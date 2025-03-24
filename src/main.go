package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tidwall/gjson"
)

// prometheus metric definitions
var (
	nettel_zmq_rcvd_messages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nettel_zmq_rcvd_messages",
		Help: "Count of gcpnettel zmq messages received.",
	}, []string{"hostname", "ifid"}) // labels for the metrics
)

var (
	nettel_flow_drops = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nettel_flow_drops",
		Help: "Count of gcpnettel netflow record drops.",
	}, []string{"hostname", "ifid"}) // labels for the metrics
)

var (
	nettel_zmq_msg_drops = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nettel_zmq_msg_drops",
		Help: "Count of gcpnettel zmq message drops.",
	}, []string{"hostname", "ifid"}) // labels for the metrics
)

var (
	nettel_zmq_avg_msg_perflow = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nettel_zmq_avg_msg_perflows",
		Help: "Count of average zmq messages per flow. This should probs be a gague however........",
	}, []string{"hostname", "ifid"}) // labels for the metrics
)

// struct to hold config values
type config struct {
	ntopngFullUrl            string
	basicAuthenticationToken string
	promPort                 string
	promEndpoint             string
}

func promExport(promPort string, promEndpoint string) {
	// Export prom metrics in a goroutine
	// Running this in parallel since http.ListenAndServe() blocks forever
	http.Handle(promEndpoint, promhttp.Handler())
	http.ListenAndServe(fmt.Sprintf(":%s", promPort), nil)
}

func parseConf() config {
	// function to parse configuration from env vars. sets default values if it cannot
	// find an env value.

	ntopngUrl, exists := os.LookupEnv("NTOPNG_API_URL")
	if exists {
		log.Println("NTOPNG_API_URL:", ntopngUrl)
	} else {
		log.Println("NTOPNG_API_URL not found. Setting to default value of http://localhost")
		ntopngUrl = "http://localhost"
	}

	ntopngPort, exists := os.LookupEnv("NTOPNG_API_PORT")
	if exists {
		log.Println("NTOPNG_API_PORT:", ntopngPort)
	} else {
		log.Println("NTOPNG_API_PORT not found. Setting to default value of 3000")
		ntopngPort = "3000"
	}

	ntopngUsername, exists := os.LookupEnv("NTOPNG_USERNAME")
	if exists {
		log.Println("NTOPNG_USERNAME:", ntopngUsername)
	} else {
		log.Println("NTOPNG_USERNAME not found. Setting to default value of admin")
		ntopngUsername = "admin"
	}

	ntopngPassword, exists := os.LookupEnv("NTOPNG_PASSWORD")
	if exists {
		log.Println("NTOPNG_PASSWORD set.")
	} else {
		log.Println("NTOPNG_PASSWORD not found. Setting to default value of admin")
		ntopngPassword = "admin"
	}

	promPort, exists := os.LookupEnv("PROMETHEUS_PORT")
	if exists {
		log.Println("PROMETHEUS_PORT:", promPort)
	} else {
		log.Println("PROMETHEUS_PORT not found. Setting to default value of 8888")
		promPort = "8888"
	}

	promEndpoint, exists := os.LookupEnv("PROMETHEUS_ENDPOINT")
	if exists {
		log.Println("PROMETHEUS_ENDPOINT:", promEndpoint)
	} else {
		log.Println("PROMETHEUS_ENDPOINT not found. Setting to default value of /metrics")
		promEndpoint = "/metrics"
	}

	ntopngFullUrl := ntopngUrl + string(':') + ntopngPort

	usernamePass := ntopngUsername + string(':') + ntopngPassword
	basicAuthenticationToken := base64.StdEncoding.EncodeToString([]byte(usernamePass))

	configuration := config{
		ntopngFullUrl:            ntopngFullUrl,
		basicAuthenticationToken: basicAuthenticationToken,
		promPort:                 promPort,
		promEndpoint:             promEndpoint,
	}

	return configuration
}

func queryNtopMetricsWithRetries(ntopngFullUrl string, basicAuthenticationToken string, ifid int) (string, error) {
	var url = fmt.Sprintf("%s/lua/rest/v2/get/interface/data.lua?ifid=%d", ntopngFullUrl, ifid)

	req, _ := http.NewRequest("GET", url, nil)

	req.Header.Set("Authorization", "Basic "+basicAuthenticationToken)

	client := &http.Client{}

	resp, err := client.Do(req)

	if err != nil {
		fmt.Println(err)

		return "nil", err

	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		log.Fatal(err)
	}
	return string(body), err

}

func queryNtopMetrics(c config, ifid int) (string, error) {
	var retries int
	var body string
	var err error
	var waitTime int

	for retries < 40 {
		body, err = queryNtopMetricsWithRetries(c.ntopngFullUrl, c.basicAuthenticationToken, ifid)
		if err == nil {
			break
		} else {
			retries += 1
			// expoential backoff. Up to 1469 seconds (about 25 minutes) on the last
			// iteration
			waitTime = 1 * int(math.Pow(1.2, float64(retries)))

			log.Printf("Error: Unable to query Ntopng API for interface time series data. Retrying with %d second backoff.", waitTime)

			time.Sleep(time.Duration(waitTime) * time.Second)
		}
	}

	return body, err

}

func enumerateInterfaceIDsWithRetries(ntopngFullUrl string, basicAuthenticationToken string) ([]int, error) {
	// hit ntopng to enumerate all interface IDs and put into a slice
	// https://www.ntop.org/guides/ntopng/api/rest/examples_v2.html#interfaces

	var url = ntopngFullUrl + "/lua/rest/v2/get/ntopng/interfaces.lua"

	req, _ := http.NewRequest("GET", url, nil)

	req.Header.Set("Authorization", "Basic "+basicAuthenticationToken)

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	var interfaces []int

	result := gjson.Get(string(body), "rsp")
	result.ForEach(func(key, value gjson.Result) bool {
		// In cases where the view:all interface is enabled, we do not wish to
		// export the view:all interface since that creates situations where the
		// prom sum() function unintuitively returns doubled values
		if gjson.Get(value.String(), "ifname").Str != "view:all" {
			retVal := gjson.Get(value.String(), "ifid")
			interfaces = append(interfaces, int(retVal.Int()))
		}
		return true // keep iterating
	})

	return interfaces, err

}

func enumerateInterfaceIDs(c config) ([]int, error) {

	var retries int
	var interfaces []int
	var err error
	var waitTime int

	for retries < 40 {
		interfaces, err = enumerateInterfaceIDsWithRetries(c.ntopngFullUrl, c.basicAuthenticationToken)
		if err == nil {
			break
		} else {
			retries += 1
			// expoential backoff. Up to 1469 seconds (about 25 minutes) on the last
			// iteration
			waitTime = 1 * int(math.Pow(1.2, float64(retries)))

			log.Printf("Error: Unable to query Ntopng API for interface data. Retrying with %d second backoff.", waitTime)

			time.Sleep(time.Duration(waitTime) * time.Second)
		}
	}

	return interfaces, err

}

func calculateCounterVal(promMetricVal uint64, ntopMetricValInt uint64) (uint64, uint64) {

	var toAdd uint64 = 0
	var counterVal uint64
	if promMetricVal < ntopMetricValInt {
		// normal incrementing behvaior of the metricVal
		toAdd = ntopMetricValInt - promMetricVal
		counterVal = ntopMetricValInt

	} else if promMetricVal > ntopMetricValInt {
		// it appears the counterVal reset...handle appropriately.
		log.Println("counterVal reset detected. Handling appropriately...")
		toAdd = ntopMetricValInt
		counterVal = ntopMetricValInt

	} else {
		// this is the case where counterVal == ntopMetricValInt which is a noop
		counterVal = ntopMetricValInt
	}

	return counterVal, toAdd

}

func scraper(ctx context.Context, name string, conf config) {

	var interfaces []int
	var err error
	interfaces, err = enumerateInterfaceIDs(conf)

	if err != nil {
		log.Println("oh no. error hitting ntopng api for interface data!")
	}

	// initialization of map with empy slices in it
	metricsMap := make(map[string][]uint64)

	metricsMap["zmq_msg_rcvd"] = []uint64{}
	metricsMap["dropped_flows"] = []uint64{}
	metricsMap["zmq_msg_drops"] = []uint64{}
	metricsMap["zmq_avg_msg_flows"] = []uint64{}

	// initialization of slices; creates as many elements as we have interface IDs
	for i := 0; i < len(interfaces); i++ {
		metricsMap["zmq_msg_rcvd"] = append(metricsMap["zmq_msg_rcvd"], 0)
		metricsMap["dropped_flows"] = append(metricsMap["dropped_flows"], 0)
		metricsMap["zmq_msg_drops"] = append(metricsMap["zmq_msg_drops"], 0)
		metricsMap["zmq_avg_msg_flows"] = append(metricsMap["zmq_avg_msg_flows"], 0)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println(name, "is stopping")
			return
		default:
			var metricVal uint64
			var toAdd uint64

			// sleep between iterations
			time.Sleep(2 * time.Second)

			log.Println("metrics map:", metricsMap)

			// iterate over all the metrics we care about
			for metricName := range metricsMap {

				// loop over all ntopng interfaces
				for i := 0; i < len(interfaces); i++ {

					var body string

					body, err = queryNtopMetrics(conf, interfaces[i])
					if err != nil {
						log.Println("oh no. error hitting ntopng api for metrics data!")
					}

					if body == "1" {
						log.Println("Error: Skipping interface")
						continue
					}

					ntopMetricVal := gjson.Get(body, fmt.Sprintf("rsp.zmqRecvStats.%s", metricName))

					ntopMetricValInt := uint64(ntopMetricVal.Int())

					// unfortuantley, counter metrics do not have a `set` method. As a result
					// we have to do a little rigamarole to
					// a) only add if we have updates AND
					// b) calculate the correct amount to add
					metricVal, toAdd = calculateCounterVal(metricsMap[metricName][i], ntopMetricValInt)

					// append to the slice held in metricsMap
					// metricsMap[metricName] = append(metricsMap[metricName], metricVal)
					metricsMap[metricName][i] = metricVal

					hostname, err := os.Hostname()
					if err != nil {
						log.Println("oh no. Unable to detect what your hostname is :shrug:")
					}

					// now update our metrics:
					switch metricName {
					case "zmq_msg_rcvd":
						nettel_zmq_rcvd_messages.WithLabelValues(hostname, fmt.Sprintf("%d", interfaces[i])).Add(float64(toAdd))
					case "dropped_flows":
						nettel_flow_drops.WithLabelValues(hostname, fmt.Sprintf("%d", interfaces[i])).Add(float64(toAdd))
					case "zmq_msg_drops":
						nettel_zmq_msg_drops.WithLabelValues(hostname, fmt.Sprintf("%d", interfaces[i])).Add(float64(toAdd))
					case "zmq_avg_msg_flows":
						nettel_zmq_avg_msg_perflow.WithLabelValues(hostname, fmt.Sprintf("%d", interfaces[i])).Add(float64(toAdd))
					default:
						log.Println("Error: Invalid data! :(")
					}
				}
			}

		}
	}
}

func main() {
	pid := os.Getpid()
	log.Printf("The PID of this process is: %d\n", pid)

	// conf is a struct with our configuration options in it
	conf := parseConf()

	// fire up the prom exporter in a goroutine since it blocks
	go promExport(conf.promPort, conf.promEndpoint)

	// Create a channel to receive signals.
	sigChan := make(chan os.Signal, 1)

	// Notify the channel for SIGINT and SIGTERM signals.
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create a context that can be cancelled.
	ctx, cancel := context.WithCancel(context.Background())

	// Start a goroutine to perform work.
	go scraper(ctx, "Task", conf)

	// Block until a signal is received.
	sig := <-sigChan
	fmt.Println("Received signal:", sig)

	// Cancel the context to signal goroutines to stop.
	cancel()

	// Wait for goroutines to finish (optional).
	time.Sleep(2 * time.Second)

	fmt.Println("Exiting...")

}
