/*
Basic Prometheus exporter for a Docker Swarm.  Exposes a HTTP endpoint for
Prometheus to scrape which just has the basic info on what services are
running and how many tasks are in what state for each service.
*/
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// Setting up the logger
var log_output = zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
var logger = zerolog.New(log_output).With().Timestamp().Logger()

func main() {
	// Set the envvar DEBUG=1 to enable debug logging.
	if os.Getenv("DEBUG") == "1" {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// Init the docker client, use the DOCKER_HOST envvar to override the OS-default.
	dockerClient, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)

	if err != nil {
		logger.Fatal().Err(err).Msgf("Error while initialising Docker client.")
	}

	// Output some useful info
	var docker_host = os.Getenv("DOCKER_HOST")
	if docker_host == "" {
		docker_host = client.DefaultDockerHost
	}
	logger.Info().Msgf("Docker client created using socket host: %s", docker_host)

	// Setup the metrics collector
	coll := DockerServices{Client: dockerClient}
	if err := prometheus.Register(&coll); err != nil {
		logger.Fatal().Err(err).Msgf("Error while registering metrics collector.")
	}

	// Test connectivity to the docker daemon
	info, err := coll.Client.Info(context.Background())

	if err != nil {
		logger.Fatal().Err(err).Msgf("Error communicating with Docker Socket.")
	}

	logger.Info().Str("OS", info.OSType+" / "+info.OperatingSystem).Str("version", info.ServerVersion).Msgf("Connected to Docker Daemon")

	// Get rid of the stupid golang metrics
	prometheus.Unregister(collectors.NewGoCollector())

	// Setup the HTTP routing
	http.Handle("/metrics", promhttp.Handler())

	// Start the HTTP server
	logger.Info().Msgf("Starting HTTP Server on port TCP/9675")
	err = http.ListenAndServe(":9675", nil)

	if err != nil {
		logger.Fatal().Err(err).Msgf("Error starting HTTP server.")
	}
}

// DockerServices implements the Collector interface.
type DockerServices struct {
	*client.Client
}

var _ prometheus.Collector = (*DockerServices)(nil)

var (
	replicaCount = prometheus.NewDesc(
		"swarm_service_desired_replicas",
		"Number of replicas requested for this service",
		[]string{"service_name"}, nil,
	)
	taskCount = prometheus.NewDesc(
		"swarm_service_tasks",
		"Number of docker tasks",
		[]string{"service_name", "state"}, nil,
	)
	containerHealthStatus = prometheus.NewDesc(
		"container_health_status",
		"Container Health Status",
		[]string{"container_health_status", "container_name"}, nil,
	)
	containerStatus = prometheus.NewDesc(
		"container_status",
		"Container status",
		[]string{"container_status", "container_name"}, nil,
	)
	imageVersion = prometheus.NewDesc(
		"swarm_service_info",
		"Information about each service",
		[]string{"service_name", "image"}, nil,
	)
	lastChangeTime = prometheus.NewDesc(
		"swarm_service_change_time",
		"Time when a task state last changed",
		[]string{"service_name"}, nil,
	)
)

func (c DockerServices) Describe(ch chan<- *prometheus.Desc) {
	ch <- replicaCount
	ch <- taskCount
	ch <- imageVersion
	ch <- lastChangeTime
}

// Collect scrapes the container information from Docker.
func (c DockerServices) Collect(ch chan<- prometheus.Metric) {
	logger.Debug().Msgf("Received request for metrics.")
	services, err := c.Client.ServiceList(context.Background(), types.ServiceListOptions{})
	if err != nil {
		logger.Fatal().Err(err).Msgf("Error listing Swarm Services.")
	}

	tasks, err := c.Client.TaskList(context.Background(), types.TaskListOptions{})
	if err != nil {
		logger.Fatal().Err(err).Msgf("Error listing Swarm Tasks.")
	}

	containers, err := c.Client.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		logger.Fatal().Err(err).Msgf("Error listing Docker Containers.")
	}

	for _, container := range containers {
		container_json, err := c.Client.ContainerInspect(context.Background(), container.ID)
		if err != nil {
			logger.Fatal().Err(err).Msgf("Error inspecting Docker Container details.")
		}

		ch <- prometheus.MustNewConstMetric(
			containerStatus,
			prometheus.GaugeValue,
			float64(1),
			container_json.State.Status,
			container_json.Name,
		)

		if container_json.State.Health != nil {
			ch <- prometheus.MustNewConstMetric(
				containerHealthStatus,
				prometheus.GaugeValue,
				float64(1),
				container_json.State.Health.Status,
				container_json.Name,
			)
		}
	}

	for _, service := range services {
		if service.Spec.Mode.Replicated != nil {
			ch <- prometheus.MustNewConstMetric(
				replicaCount,
				prometheus.GaugeValue,
				float64(*service.Spec.Mode.Replicated.Replicas),
				service.Spec.Annotations.Name,
			)
		}

		taskStates := make(map[string]int)
		var lastTaskStatusChange time.Time
		for _, task := range tasks {
			if task.ServiceID == service.ID {
				taskStates[string(task.Status.State)] += 1
				if task.Status.Timestamp.After(lastTaskStatusChange) {
					lastTaskStatusChange = task.Status.Timestamp
				}
			}
		}

		for state, count := range taskStates {
			ch <- prometheus.MustNewConstMetric(
				taskCount,
				prometheus.GaugeValue,
				float64(count),
				service.Spec.Annotations.Name,
				string(state),
			)
		}

		// See https://www.robustperception.io/exposing-the-software-version-to-prometheus
		ch <- prometheus.MustNewConstMetric(
			imageVersion,
			prometheus.GaugeValue,
			1,
			service.Spec.Annotations.Name,
			string(service.Spec.TaskTemplate.ContainerSpec.Image),
		)

		ch <- prometheus.MustNewConstMetric(
			lastChangeTime,
			prometheus.GaugeValue,
			float64(lastTaskStatusChange.Unix()),
			service.Spec.Annotations.Name,
		)
	}
}
