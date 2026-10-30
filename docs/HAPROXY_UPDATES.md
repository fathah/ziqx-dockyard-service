# Seamless updates behind an HAProxy container

The API service can keep `deploy.replicas: 2` and have no published ports. HAProxy remains the public entry point; Dockyard starts two replacement API replicas, checks each, waits for HAProxy to discover and accept both, drains the previous API backends, and stops them only after their active request count reaches zero. Databases, workers, migrations, the load balancer and Caddy keep running during subsequent API image updates.

This requires VPS service **0.7.37+** and desktop **0.1.9+**. Older installed servers display **Individual updates currently support one container per service**. Install the matching Dockyard server through **Server details → Updates** and refresh Services. That server update leaves application containers running. A saved configuration draft alone does not change the deployed update eligibility.

## One-time Tanseek configuration

Add these labels to `services.api` in the deployed Compose configuration:

```yaml
services:
  api:
    labels:
      io.ziqx.dockyard.haproxy.service: loadbalancer
      io.ziqx.dockyard.haproxy.backend: api_replicas
      io.ziqx.dockyard.haproxy.port: "8080"
    deploy:
      replicas: 2
```

Keep the API's existing healthcheck, dependencies and networks. Add the Runtime API listener to the existing `global` section of `haproxy.cfg`:

```haproxy
global
  stats socket ipv4@127.0.0.1:9999 level admin
```

The listener binds only to loopback **inside the HAProxy container**. Do not publish port 9999 in Compose. Dockyard accesses it with `docker exec` in the positively identified load balancer container. The proxy image needs `sh` and `nc`; the tested `haproxy:3.2.23-alpine` image includes both. If using a different control port, add the optional label `io.ziqx.dockyard.haproxy.admin-port`.

Retain Tanseek's Docker resolver and backend discovery:

```haproxy
resolvers docker
  nameserver docker 127.0.0.11:53
  accepted_payload_size 8192
  hold valid 5s
  hold obsolete 5s
  hold nx 3s
  hold timeout 3s
  hold refused 3s
  timeout resolve 1s
  timeout retry 1s

backend api_replicas
  balance leastconn
  option httpchk
  http-check send meth GET uri /health/ready ver HTTP/1.1 hdr Host api
  http-check expect status 200
  server-template api 1-16 api:8080 check inter 3s fall 2 rise 2 resolvers docker resolve-prefer ipv4 init-addr none
```

The template needs room for both groups during overlap: two current plus two replacement replicas need at least four slots. The API and load balancer must share exactly one Docker network. Keep other API networks for databases and validators. This integration expects traffic to reach the API through this proxy and a backend without persistence rules that bypass drain state.

Apply this setup once so the labels are in Dockyard's active configuration and the private Runtime API is running. A **Controlled restart** configuration deployment can apply it; schedule that initial maintenance separately. Then open **Services → api → Pull & update → Seamless updates**, use container port `8080`, review the two-replica HAProxy plan, and submit. Subsequent image updates use the new path without restarting HAProxy.

## Failures and recovery

Preview reads HAProxy state and validates the owned container, backend, network and current API addresses before accepting an update. A disabled Runtime API, wrong backend or unhealthy old replica blocks review without changing containers.

If a candidate fails before receiving service aliases, the old replicas remain running. Once DNS can expose candidates, an uncertain operation retains both groups and requires recovery. Recovery checks actual proxy state because the public Caddy route is identical before and after an internal backend update. Healthy candidates can complete the switch; otherwise verified healthy old replicas are restored before candidate cleanup. A proxy identity or network change blocks recovery rather than controlling an unreviewed proxy.

After the configured drain interval, active requests on an old backend prevent retirement. Let those requests finish, then retry the normal local reconciliation procedure. A timer expiring alone never authorizes stopping a backend with active sessions.

HAProxy's [DNS resolution guide](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/proxying-essentials/dns-resolution/) documents discovery; its [Runtime API](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/set-server/) documents ready/drain state.
