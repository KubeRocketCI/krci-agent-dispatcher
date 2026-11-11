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
| agentImage | string | `""` | Agent image of the issue actors: `<repo>[:<tag>]@sha256:<digest>`. Required. |
| config.botUsername | string | `"krci-agent"` | Bot user: ignored note author, marker author. |
| config.pollInterval | string | `"10s"` | Marker poll interval. |
| config.triggerLabel | string | `"agent"` | Label that enables the agent. |
| config.turnLimit | int | `20` | Turn limit per issue. |
| config.turnTimeout | string | `"15m"` | Dispatcher wait for one turn's marker. |
| fullnameOverride | string | `""` | Overrides the full name. |
| gitlab.caConfigMap | string | `""` | ConfigMap with key ca.crt: extra CA bundle for GitLab TLS. Empty: system roots. |
| gitlab.url | string | `""` | GitLab base URL: `https://HOST[:PORT][/PATH]`, for example https://gitlab.example.com. No user info, query or fragment. Trailing slash allowed. Required. |
| image.digest | string | `""` | Image digest (sha256:...); takes precedence over image.tag. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `""` | Dispatcher image repository. Required. |
| image.tag | string | `""` | Image tag. Set image.tag or image.digest. |
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
| resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}` | Container resources. |
| secrets.gitlab | string | `"gitlab"` | Secret with key token: GitLab token of the bot user, mounted as a file and read on each request. |
| secrets.hook | string | `"hook"` | Secret with key token: GitLab hook secret token. |
| service.port | int | `8080` | Service port. |
| serviceAccount.annotations | object | `{}` | Annotations of the ServiceAccount. Its name is the full name: the Substrate access policy member. |
| substrate.addr | string | `"api.ate-system.svc:443"` | Substrate API address. |
| substrate.atespace | string | `""` | Atespace of the issue actors. Empty: the release namespace. |
| substrate.pool | string | `"workers"` | Worker pool of the actor template. |
| substrate.secretsNamespace | string | `""` | Namespace in the ate-secret:// URIs of the egress policy. Empty: the release namespace. |
| substrate.tokenAudience | string | `"api.ate-system.svc"` | ServiceAccount token audience of the Substrate API. |
| substrate.trustBundleLabels | object | `{"podcert.ate.dev/canarying":"live"}` | ClusterTrustBundle label selector. |
| substrate.trustBundleSigner | string | `"servicedns.podcert.ate.dev/identity"` | ClusterTrustBundle signer of the Substrate API serving certificate. |
| tolerations | list | `[]` | Tolerations. |
