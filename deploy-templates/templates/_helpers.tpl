{{/*
Expand the name of the chart.
*/}}
{{- define "krci-agent-dispatcher.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "krci-agent-dispatcher.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "krci-agent-dispatcher.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "krci-agent-dispatcher.labels" -}}
helm.sh/chart: {{ include "krci-agent-dispatcher.chart" . }}
{{ include "krci-agent-dispatcher.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "krci-agent-dispatcher.selectorLabels" -}}
app.kubernetes.io/name: {{ include "krci-agent-dispatcher.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Image reference: repository@digest, else repository:tag, else repository:appVersion.
*/}}
{{- define "krci-agent-dispatcher.image" -}}
{{- $repo := required "image.repository is required" .Values.image.repository }}
{{- if .Values.image.digest }}
{{- printf "%s@%s" $repo .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" $repo (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}
{{- end }}

{{/*
Dispatcher credentials: the Secret only the dispatcher reads, keys gitlab-token and hook-token.
*/}}
{{- define "krci-agent-dispatcher.secret.dispatcher" -}}
{{- include "krci-agent-dispatcher.fullname" . }}
{{- end }}

{{/*
Actor credentials: the Secrets only the egress gateway reads. Names fixed by internal/substrate/templates/egresspolicy.yaml.tmpl.
*/}}
{{- define "krci-agent-dispatcher.secret.gitlab" -}}gitlab{{- end }}
{{- define "krci-agent-dispatcher.secret.anthropic" -}}anthropic{{- end }}

{{/*
Label of every actor credential: the secretSelector of the Substrate namespace policy grant of the atespace.
*/}}
{{- define "krci-agent-dispatcher.actorCredentialLabels" -}}
app.edp.epam.com/actor-credential: "true"
{{- end }}

{{/*
Image pinned by digest, <repo>[:<tag>]@sha256:<digest>, from the value named by the argument, for example harnessImage.
*/}}
{{- define "krci-agent-dispatcher.pinnedImage" -}}
{{- $name := index . 1 }}
{{- $image := required (printf "%s is required" $name) (index (index . 0).Values $name) }}
{{- if not (regexMatch "^[^[:space:]@]+@sha256:[a-f0-9]{64}$" $image) }}
{{- fail (printf "%s must be pinned by digest: <repo>[:<tag>]@sha256:<digest>" $name) }}
{{- end }}
{{- $image }}
{{- end }}
