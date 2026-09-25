{{/*
Copyright The Linux Foundation and each contributor to LFX.
SPDX-License-Identifier: MIT
*/}}

{{/*
Common labels applied to every rendered object.
*/}}
{{- define "lfx-v2-newsletter-service.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/*
Selector labels — must remain stable across upgrades (immutable on Deployments).
*/}}
{{- define "lfx-v2-newsletter-service.selectorLabels" -}}
app: {{ .Chart.Name }}
{{- end }}

{{/*
Name of the CloudNativePG app-user Secret (used by the service for DATABASE_URL).
Derived from database.cloudNativePG.clusterName.
*/}}
{{- define "lfx-v2-newsletter-service.cloudNativePGAppSecret" -}}
{{- printf "%s-app" .Values.database.cloudNativePG.clusterName }}
{{- end }}
