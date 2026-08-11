import { getWebInstrumentations, initializeFaro } from "@grafana/faro-web-sdk";
import { TracingInstrumentation } from "@grafana/faro-web-tracing";
import { ReplayInstrumentation } from "@grafana/faro-instrumentation-replay";

(() => {
  if (typeof window === "undefined") {
    return;
  }

  const collectorEndpoint = {{ strings.TrimSpace (.Site.Params.faro.collectorendpoint | default "") | jsonify }};
  if (!collectorEndpoint) {
    return;
  }

  initializeFaro({
    url: collectorEndpoint,
    app: {
      name: {{ .Site.Params.faro.appname | default "graith-docs" | jsonify }},
      version: {{ .Site.Params.faro.appversion | default "1.0.0" | jsonify }},
      environment: {{ .Site.Params.faro.environment | default "production" | jsonify }},
    },
    sessionTracking: {
      samplingRate: 1,
    },
    instrumentations: [
      ...getWebInstrumentations({
        captureConsole: true,
        enablePerformanceInstrumentation: true,
        enableContentSecurityPolicyInstrumentation: true,
      }),
      new TracingInstrumentation(),
      new ReplayInstrumentation({
        samplingRate: 1,
        maskAllInputs: false,
        maskInputOptions: {},
        maskTextSelector: undefined,
        recordAfter: "load",
        recordCrossOriginIframes: false,
      }),
    ],
  });
})();
