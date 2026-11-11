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
Image reference: repository@digest, else repository:tag.
*/}}
{{- define "krci-agent-dispatcher.image" -}}
{{- $repo := required "image.repository is required" .Values.image.repository }}
{{- if .Values.image.digest }}
{{- printf "%s@%s" $repo .Values.image.digest }}
{{- else if .Values.image.tag }}
{{- printf "%s:%s" $repo .Values.image.tag }}
{{- else }}
{{- fail "image.tag or image.digest is required" }}
{{- end }}
{{- end }}

{{/*
Agent image: <repo>[:<tag>]@sha256:<digest>.
*/}}
{{- define "krci-agent-dispatcher.agentImage" -}}
{{- $image := required "agentImage is required" .Values.agentImage }}
{{- if not (regexMatch "^[^[:space:]@]+@sha256:[a-f0-9]{64}$" $image) }}
{{- fail "agentImage must be pinned by digest: <repo>[:<tag>]@sha256:<digest>" }}
{{- end }}
{{- $image }}
{{- end }}
