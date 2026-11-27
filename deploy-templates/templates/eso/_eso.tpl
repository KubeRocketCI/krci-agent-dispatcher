{{/*
ESO Secrets: one ExternalSecret each.
  name:     Secret name.
  scope:    credential scope, dispatcher or actor: the provider secret at eso.secretPath.<scope>.
  labels:   optional; Secret labels.
  data:     Secret keys from properties of the provider secret.
  template: optional; ESO template data (engine v2) that sets the Secret keys; reads the data keys as .<key>.
            No template: the Secret keys are the data keys.
*/}}
{{- define "krci-agent-dispatcher.eso.secrets" -}}
secrets:
  - name: {{ include "krci-agent-dispatcher.secret.dispatcher" . }}
    scope: dispatcher
    data:
      - key: gitlab-token
        property: gitlab-token
      - key: hook-token
        property: hook-token
  - name: {{ include "krci-agent-dispatcher.secret.gitlab" . }}
    scope: actor
    labels:
      {{- include "krci-agent-dispatcher.actorCredentialLabels" . | nindent 6 }}
    data:
      - key: token
        property: gitlab-token
    template:
      token: {{ `{{ .token }}` | squote }}
      # basic: base64 of oauth2:<token>.
      basic: {{ `{{ printf "oauth2:%s" .token | b64enc }}` | squote }}
  - name: {{ include "krci-agent-dispatcher.secret.anthropic" . }}
    scope: actor
    labels:
      {{- include "krci-agent-dispatcher.actorCredentialLabels" . | nindent 6 }}
    data:
      - key: api-key
        property: anthropic-api-key
{{- end }}

{{/*
ESO ServiceAccount name, for providers aws and vault.
*/}}
{{- define "krci-agent-dispatcher.eso.serviceAccount" -}}
{{- printf "externalsecrets-%s-%s" (include "krci-agent-dispatcher.fullname" .) .Values.eso.provider | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ESO checks. Empty output; fails on invalid values.
*/}}
{{- define "krci-agent-dispatcher.eso.validate" -}}
{{- if not (has .Values.eso.provider (list "aws" "generic" "vault")) }}
{{- fail "eso.provider must be one of aws, generic, vault" }}
{{- end }}
{{- $_ := required "eso.secretPath.dispatcher is required" .Values.eso.secretPath.dispatcher }}
{{- $_ = required "eso.secretPath.actor is required" .Values.eso.secretPath.actor }}
{{- if and (eq .Values.eso.provider "generic") (empty .Values.eso.generic.secretStore.providerConfig) }}
{{- fail "eso.generic.secretStore.providerConfig is required when eso.provider is generic" }}
{{- end }}
{{- end }}
