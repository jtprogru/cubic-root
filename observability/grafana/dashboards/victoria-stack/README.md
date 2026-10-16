# victoria-stack dashboards

Official dashboards for self-monitoring of the local stack, copied as is from the `dashboards/vm/` directories (the variants for the VictoriaMetrics Grafana datasource):

| file | source |
|---|---|
| `victoriametrics.json` | `VictoriaMetrics/VictoriaMetrics@v1.154.0` |
| `vmagent.json` | `VictoriaMetrics/VictoriaMetrics@v1.154.0` |
| `vmalert.json` | `VictoriaMetrics/VictoriaMetrics@v1.154.0` |
| `victorialogs.json` | `VictoriaMetrics/VictoriaLogs@v1.53.0` |
| `victoriatraces.json` | `VictoriaMetrics/VictoriaTraces@v0.12.0` |

When bumping an image in `compose.yaml`, refresh the matching dashboard from the same tag.
