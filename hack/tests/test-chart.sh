#!/usr/bin/env bash
# Render tests for deploy-templates (helm template + yq).
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${HERE}/assert.sh"
CHART="${HERE}/../../deploy-templates"
DIGEST="sha256:$(printf '%064d' 1)"

BASE=(--set image.repository=registry.example/krci-agent-dispatcher --set "image.digest=${DIGEST}"
  --set agentImage=registry.example/krci-agent-go@sha256:abc --set gitlab.url=https://gitlab.example
  --set ingress.host=agent-dispatcher.example --set ingress.tls.issuerRef.kind=Issuer --set ingress.tls.issuerRef.name=ca)

render() { OUT="$(helm template krci-agent-dispatcher "${CHART}" -n krci-agents "$@" 2>&1)"; }
doc() { yq "select(.kind == \"$1\")" <<<"${OUT}"; }
envv() { doc Deployment | yq ".spec.template.spec.containers[0].env[] | select(.name == \"$1\") | ${2:-.value}"; }

test_requires_values() {
  local v
  for v in ingress.host ingress.tls.issuerRef.name agentImage gitlab.url image.repository; do
    render "${BASE[@]}" --set "${v}="; assert_eq 1 $?
    assert_contains "${v}" "${OUT}"
  done
  render "${BASE[@]}" --set image.digest=; assert_eq 1 $?; assert_contains 'image.tag or image.digest' "${OUT}"
}

test_deployment_env_and_secrets() {
  render "${BASE[@]}" --set gitlab.caConfigMap=gitlab-ca; assert_eq 0 $?
  assert_eq krci-agents "$(envv ATESPACE)"
  assert_eq krci-agents "$(envv SECRETS_NAMESPACE)"
  assert_eq workers "$(envv POOL)"
  assert_eq api.ate-system.svc:443 "$(envv ATE_API_ADDR)"
  assert_eq /var/run/ate/token "$(envv ATE_TOKEN_FILE)"
  assert_eq /var/run/ate/ca.crt "$(envv ATE_CA_FILE)"
  assert_eq /etc/gitlab-ca/ca.crt "$(envv GITLAB_CA_FILE)"
  assert_eq https://gitlab.example "$(envv GITLAB_URL)"
  assert_eq registry.example/krci-agent-go@sha256:abc "$(envv AGENT_IMAGE)"
  assert_eq 15m "$(envv TURN_TIMEOUT)"; assert_eq 10s "$(envv POLL_INTERVAL)"; assert_eq 20 "$(envv MAX_TURNS_PER_ISSUE)"
  assert_eq /var/run/gitlab/token "$(envv GITLAB_TOKEN_FILE)"
  assert_eq '' "$(doc Deployment | yq '.spec.template.spec.containers[0].env[] | select(.name == "GITLAB_TOKEN") | .name')"
  assert_eq 'gitlab/token' "$(doc Deployment | yq '.spec.template.spec.volumes[] | select(.name == "gitlab-token") | .secret.secretName + "/" + .secret.items[0].key')"
  assert_eq /var/run/gitlab "$(doc Deployment | yq '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "gitlab-token") | .mountPath')"
  assert_eq 'hook/token' "$(envv HOOK_TOKEN '.valueFrom.secretKeyRef | .name + "/" + .key')"
  assert_eq "registry.example/krci-agent-dispatcher@${DIGEST}" "$(doc Deployment | yq '.spec.template.spec.containers[0].image')"
  assert_eq gitlab-ca "$(doc Deployment | yq '.spec.template.spec.volumes[] | select(.name == "gitlab-ca") | .configMap.name')"
}

test_projected_ate_volume() {
  render "${BASE[@]}"; assert_eq 0 $?
  local v; v="$(doc Deployment | yq '.spec.template.spec.volumes[] | select(.name == "ate") | .projected.sources')"
  assert_eq servicedns.podcert.ate.dev/identity "$(yq '.[] | select(.clusterTrustBundle) | .clusterTrustBundle.signerName' <<<"$v")"
  assert_eq live "$(yq '.[] | select(.clusterTrustBundle) | .clusterTrustBundle.labelSelector.matchLabels["podcert.ate.dev/canarying"]' <<<"$v")"
  assert_eq ca.crt "$(yq '.[] | select(.clusterTrustBundle) | .clusterTrustBundle.path' <<<"$v")"
  assert_eq api.ate-system.svc "$(yq '.[] | select(.serviceAccountToken) | .serviceAccountToken.audience' <<<"$v")"
  assert_eq token "$(yq '.[] | select(.serviceAccountToken) | .serviceAccountToken.path' <<<"$v")"
  assert_eq /var/run/ate "$(doc Deployment | yq '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "ate") | .mountPath')"
}

test_no_ca_configmap() {
  render "${BASE[@]}"; assert_eq 0 $?
  assert_eq '' "$(envv GITLAB_CA_FILE)"
  assert_eq '' "$(doc Deployment | yq '.spec.template.spec.volumes[] | select(.name == "gitlab-ca") | .name')"
}

test_pod_shape() {
  render "${BASE[@]}"; assert_eq 0 $?
  local d; d="$(doc Deployment)"
  assert_eq krci-agent-dispatcher "$(yq '.spec.template.spec.serviceAccountName' <<<"$d")"
  assert_eq krci-agent-dispatcher "$(doc ServiceAccount | yq '.metadata.name')"
  assert_eq 1 "$(yq '.spec.replicas' <<<"$d")"
  assert_eq Recreate "$(yq '.spec.strategy.type' <<<"$d")"
  assert_eq 75 "$(yq '.spec.template.spec.terminationGracePeriodSeconds' <<<"$d")"
  assert_eq false "$(yq '.spec.template.spec.automountServiceAccountToken' <<<"$d")"
  assert_eq true "$(yq '.spec.template.spec.securityContext.runAsNonRoot' <<<"$d")"
  assert_eq true "$(yq '.spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem' <<<"$d")"
  assert_eq false "$(yq '.spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation' <<<"$d")"
  assert_eq /healthz "$(yq '.spec.template.spec.containers[0].readinessProbe.httpGet.path' <<<"$d")"
  assert_eq /healthz "$(yq '.spec.template.spec.containers[0].livenessProbe.httpGet.path' <<<"$d")"
  assert_eq 8080 "$(doc Service | yq '.spec.ports[0].targetPort')"
}

test_ingress_and_certificate() {
  render "${BASE[@]}"; assert_eq 0 $?
  assert_eq agent-dispatcher.example "$(doc Ingress | yq '.spec.rules[0].host')"
  assert_eq nginx "$(doc Ingress | yq '.spec.ingressClassName')"
  assert_eq /hook "$(doc Ingress | yq '.spec.rules[0].http.paths[0].path')"
  assert_eq krci-agent-dispatcher-tls "$(doc Ingress | yq '.spec.tls[0].secretName')"
  assert_eq krci-agent-dispatcher-tls "$(doc Certificate | yq '.spec.secretName')"
  assert_eq 'Issuer/ca' "$(doc Certificate | yq '.spec.issuerRef.kind + "/" + .spec.issuerRef.name')"
  assert_eq agent-dispatcher.example "$(doc Certificate | yq '.spec.dnsNames[0]')"
}

test_ingress_disabled() {
  render "${BASE[@]}" --set ingress.enabled=false --set ingress.host= --set ingress.tls.issuerRef.name=; assert_eq 0 $?
  assert_eq '' "$(doc Ingress)"; assert_eq '' "$(doc Certificate)"
}

test_values_override() {
  render "${BASE[@]}" --set substrate.atespace=team-a --set substrate.secretsNamespace=team-a-secrets --set config.maxTurnsPerIssue=5
  assert_eq 0 $?
  assert_eq team-a "$(envv ATESPACE)"; assert_eq team-a-secrets "$(envv SECRETS_NAMESPACE)"; assert_eq 5 "$(envv MAX_TURNS_PER_ISSUE)"
}

run_tests
