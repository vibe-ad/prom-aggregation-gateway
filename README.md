## Vibe specific documentation
We currently use a home made version of the prom-aggregation-gateway.
Currently the process is pretty manual and we may want to study an alternative or update this component.
### Build the image

Note that the ECR repository has been created using our Gitops workflow [here](https://github.com/vibe-ad/gitops/blob/main/crd-operators/mgmt-top-hermit/custom-resources/crossplane/ecr/prom-aggregation-gateway.yaml)

Build the image
```console
cd cmd/prom-aggregation-gateway/
docker build . -t 366938945728.dkr.ecr.us-east-1.amazonaws.com/prom-aggregation-gateway:$(git rev-parse --short HEAD)
```


Push it
```console
# Login to our AWS account
aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 366938945728.dkr.ecr.us-east-1.amazonaws.com

docker push 366938945728.dkr.ecr.us-east-1.amazonaws.com/prom-aggregation-gateway:$(git rev-parse --short HEAD)
```

# Prometheus Aggregation Gateway
Prometheus Aggregation Gateway is a aggregating push gateway for Prometheus.  As opposed to the official [Prometheus Pushgateway](https://github.com/prometheus/pushgateway), this service aggregates the sample values it receives.

* Counters where all labels match are added up.
* Histograms are added up; if bucket boundaries are mismatched then the result has the union of all buckets and counts are given to the lowest bucket that fits.
* Gauges only keep last value
* Summaries are discarded.

## How to use

Send metrics in [Prometheus format](https://prometheus.io/docs/instrumenting/exposition_formats/) to `/metrics/`

E.g. if you have the program running locally:

```bash
echo 'http_requests_total{method="post",code="200"} 1027' | curl --data-binary @- http://localhost/metrics/
```

Now you can push your metrics using your favorite Prometheus client.

E.g. in Python using [prometheus/client_python](https://github.com/prometheus/client_python):

```python
from prometheus_client import CollectorRegistry, Counter, push_to_gateway
registry = CollectorRegistry()
counter = Counter('some_counter', "A counter", registry=registry)
counter.inc()
push_to_gateway('localhost', job='my_job_name', registry=registry)
```

Then have your Prometheus scrape metrics at `/metrics`.

## Ready-built images

Available on DockerHub `weaveworks/prom-aggregation-gateway`

## Comparison to [Prometheus Pushgateway](https://github.com/prometheus/pushgateway)

According to https://prometheus.io/docs/practices/pushing/:

> The Pushgateway never forgets series pushed to it and will expose them to Prometheus forever...
>
> The latter point is especially relevant when multiple instances of a job differentiate their metrics in the Pushgateway via an instance label or similar.

This restriction makes the Prometheus pushgateway inappropriate for the usecase of accepting metrics from a client-side web app, so we created this one to aggregate counters from multiple senders.

Prom-aggregation-gateway presents a similar API, but does not attempt to be a drop-in replacement.

## JS Client Library

See https://github.com/weaveworks/promjs/ for a JS client library for Prometheus that can be used from within a web app.

## <a name="help"></a>Getting Help

If you have any questions about, feedback for or problems with `prom-aggregation-gateway`:

- Invite yourself to the <a href="https://slack.weave.works/" target="_blank">Weave Users Slack</a>.
- Ask a question on the [#general](https://weave-community.slack.com/messages/general/) slack channel.
- [File an issue](https://github.com/weaveworks/prom-aggregation-gateway/issues/new).

Your feedback is always welcome!
