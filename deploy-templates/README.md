# krci-agent-dispatcher

![Version: 0.1.0-SNAPSHOT](https://img.shields.io/badge/Version-0.1.0--SNAPSHOT-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0-SNAPSHOT](https://img.shields.io/badge/AppVersion-0.1.0--SNAPSHOT-informational?style=flat-square)

A Helm chart for krci-agent-dispatcher, the dispatcher for KubeRocketCI issue agents

**Homepage:** <https://docs.kuberocketci.io/>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| epmd-edp | <SupportEPMD-EDP@epam.com> | <https://solutionshub.epam.com/solution/kuberocketci> |

## Source Code

* <https://github.com/KubeRocketCI/krci-agent-dispatcher>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity. |
| config.agentModel | string | `"claude-haiku-5-5"` | Claude model of the agent runs. |
| config.agentSteps | int | `40` | Agent steps per turn: the Claude Code `--max-turns` of one agent run. |
| config.botUsername | string | `"krci-agent"` | Bot user: ignored note author, marker author. |
| config.harnessPollInterval | string | `"15s"` | Note poll interval of the harness in each actor, whole seconds. turnTimeout must be at least this plus 2m. |
| config.pollInterval | string | `"10s"` | Marker poll interval. |
| config.sweepInterval | string | `"5m"` | Time between sweeps over actors without an issue lane. 0 turns the sweep off. |
| config.triggerMinRole | string | `"developer"` | Trigger role: lowest GitLab project role that can start the agent and write trigger comments: developer, maintainer or owner. |
| config.turnLimit | int | `20` | Turn limit per issue. |
| config.turnTimeout | string | `"15m"` | Dispatcher wait for one turn's marker, whole seconds. The harness cuts a turn at turnTimeout minus harnessPollInterval minus 60s. |
| fullnameOverride | string | `""` | Overrides the full name. |
| gitlab.caConfigMap | string | `""` | ConfigMap with key ca.crt: extra CA bundle for GitLab TLS. Empty: system roots. |
| gitlab.url | string | `""` | GitLab base URL: `https://HOST[:PORT][/PATH]`, for example https://gitlab.example.com. No user info, query or fragment. Trailing slash allowed. Required. |
| harnessImage | string | `"epamedp/krci-agent-harness:0.1.0-SNAPSHOT.37@sha256:42be0e0f7dd47cf60efceb5d847666dc2f388e58b968be65bd6006eb03c7cca8"` | Harness image mounted into every issue actor at /opt/krci-agent: `<repo>[:<tag>]@sha256:<digest>`. Default: the last harness verified with this chart; bump it with each verified release. Override for a local build. |
| image.digest | string | `""` | Image digest (sha256:...); takes precedence over image.tag. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"epamedp/krci-agent-dispatcher"` | Dispatcher image repository. |
| image.tag | string | `""` | Image tag. Empty: the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| ingress.annotations | object | `{}` | Extra Ingress annotations. |
| ingress.className | string | `"nginx"` | Ingress class. |
| ingress.enabled | bool | `true` | Expose POST /hook through an Ingress with a cert-manager Certificate. |
| ingress.host | string | `""` | Hook host name. Required when ingress.enabled. |
| ingress.tls.issuerRef.kind | string | `"ClusterIssuer"` | cert-manager issuer kind (Issuer or ClusterIssuer). |
| ingress.tls.issuerRef.name | string | `""` | cert-manager issuer name. Required when ingress.enabled. |
| ingress.tls.secretName | string | `"krci-agent-dispatcher-tls"` | Secret of the Ingress certificate. |
| nameOverride | string | `""` | Overrides the chart name. |
| nodeSelector | object | `{}` | Node selector. |
| podAnnotations | object | `{}` | Pod annotations. |
| profiles.existingConfigMap | string | `""` | ConfigMap with key profiles.yaml: the agent profiles (agent image, egress, env per profile, project mapping, default profile). Empty: the chart's deploy-templates/profiles.yaml, embedded verbatim. Read at each actor create; a change needs no restart and applies to new sessions. |
| resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}` | Container resources. |
| secrets.gitlab | string | `"gitlab"` | Secret with key token: GitLab token of the bot user, mounted as a file and read on each request. |
| secrets.hook | string | `"hook"` | Secret with key token: GitLab hook secret token. |
| service.port | int | `8080` | Service port. |
| serviceAccount.annotations | object | `{}` | Annotations of the ServiceAccount. Its name is the full name: the Substrate access policy member. |
| substrate.addr | string | `"api.ate-system.svc:443"` | Substrate API address. |
| substrate.atespace | string | `""` | Atespace of the issue actors. Empty: the release namespace. One dispatcher per atespace: the dispatcher owns every actor named `issue-<project-id>-<iid>` in it. |
| substrate.secretsNamespace | string | `""` | Namespace in the ate-secret:// URIs of the egress policy. Empty: the release namespace. |
| substrate.tokenAudience | string | `"api.ate-system.svc"` | ServiceAccount token audience of the Substrate API. |
| substrate.trustBundleLabels | object | `{"podcert.ate.dev/canarying":"live"}` | ClusterTrustBundle label selector. |
| substrate.trustBundleSigner | string | `"servicedns.podcert.ate.dev/identity"` | ClusterTrustBundle signer of the Substrate API serving certificate. |
| substrate.workerSelector | object | `{}` | Worker selector of the actor template: match labels, key to value. A worker hosts agent actors only when every key equals its worker label, the `metadata.labels` of its WorkerPool; the WorkerPool name is not matched. Kubernetes label syntax; every value a non-empty string, numbers and booleans quoted (`--set-string` on the command line). Empty: `workload: workers`. |
| tolerations | list | `[]` | Tolerations. |
