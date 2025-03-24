# Prometheus exporter for NtopNG

## Purpose
The purpose of this program is to hit the ntopng API and extract time series metrics and convert into prometheus data. 



## Supported metrics
Currently we support a (small) subset of the metrics from ntopng:
* `zmq_msg_rcvd`
* `dropped_flows` 
* `zmq_msg_drops`
* `zmq_avg_msg_flows`

Extending to other metrics should not be that difficult. File an issue or open a PR if you are interested in other metrics.


## How it works
The `queryNtopAPI()` function hits the ntopng api endpoint `http://localhost:8080/lua/rest/v2/get/interface/data.lua?ifid=0`.

A json object is returned via the api. This is parsed using `github.com/tidwall/gjson` and then exported using `github.com/prometheus/client_golang/prometheus`.


## An Unfortunate Small amount of complexity

Unfortuantley, counter metrics do not have a `set` method. As a result we have to do a little rigamarole to sync up the counters.
a) only add if we have updates AND
b) calculate the correct amount to add
Hence why the logic is seemingly a little more complex than one would expect.

There is also kina a weird edge case where the golang client cannot reset the counter without de-registering the metric then re-registering it. 

## A quick caveat
When the ntopng service restarts, or you click on the red `Reset Counters` button in the UI, NtopNG counters will reset to 0. I was unable to find a clean way to support this since resetting the prom counter to 0 can only be accomplished by de-registering and re-registering the counter. This seems like it could cause race conditions with prom scrapes and generally doens't seem like a good design pattern.
Correspondingly, we designed our exporter to handle cases where we see counters drop by not restarting the counter and continuing to count atop the prior value. This is analogous with how prometheus `rate()` handles counter resets.
In other words, there is a slight delta to how we export in this respect, however `rate()` performs this sort of normalization anyways so after you apply `rate()` there should be no difference.
If we had the ability to set the counter to 0 or reset the counter, this would be a non-issue.
Further as a result, the only time you should observe the counter drop is if the prom exporter service itself restarts.



## Configuring and operation
The prom exporter is configured using environment variables. Configuration options are as follows:

| Environment Variable           | Description                                                          | Default Value         | 
| --------                       | -------                                                              | -------               |
| `NTOPNG_API_URL`               | ntopNG url api                                                       | `http://localhost`    | 
| `NTOPNG_API_PORT`              | The tcp port used by ntopNG's api                                    | `3000`                | 
| `NTOPNG_USERNAME`              | Ntopng username used to authenticate to the API                      | `admin`               |
| `NTOPNG_PASSWORD`              | Password used by the `NTOPNG_USERNAME` to authenticate to the api    | `admin`               |
| `PROMETHEUS_PORT`              | Port the prometheus listener listens on.                             | `8888`                | 
| `PROMETHEUS_ENDPOINT`          | HTTP endpoint the exporter publishes messages on.                    | `/metrics`            |



## One other caveat
The prom exporter enumerates active ntopng interfaces at startup. Thus if you add/remove ntopng interfaces, you should also restart the exporter. With ntopng, you must restart the service to add/remove interfaces; thus it makes sense to 

Using systemd unitfiles, you could leverage `PartOf` to trigger a restart of your exporter service when ntopng restarts. For config management systems like chef, you could alternatively use a `nofity` to inform the service that it should restart. If running in Kubernetes, you could run the exporter in a sidecar container which will terminate upon ntopng container termination.