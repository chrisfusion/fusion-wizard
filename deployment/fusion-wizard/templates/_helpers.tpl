{{/*
SPDX-License-Identifier: GPL-3.0-or-later
Common labels applied to every resource. The chart label replaces "+" because Flux appends build
metadata (e.g. 0.1.0+3) to the chart version and "+" is not valid in a label value.
*/}}
{{- define "fusion-wizard.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "fusion-wizard.operator.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: operator
{{- end }}

{{- define "fusion-wizard.api.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: api
{{- end }}

{{- define "fusion-wizard.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end }}

{{/* Name of the per-instance settings ConfigMap. */}}
{{- define "fusion-wizard.configName" -}}
{{ .Release.Name }}-config
{{- end }}

{{/*
Upstream base URL: the explicit value, or the service's usual in-cluster address in the release
namespace. Call with (list . <values.upstreams.x> <service name> <port>).
*/}}
{{- define "fusion-wizard.upstreamURL" -}}
{{- $root := index . 0 -}}
{{- $up := index . 1 -}}
{{- if $up.url -}}
{{ $up.url }}
{{- else -}}
http://{{ index . 2 }}.{{ $root.Release.Namespace }}.svc.cluster.local:{{ index . 3 }}
{{- end -}}
{{- end }}

{{/* Projected service-account token volumes, one per upstream with auth enabled. */}}
{{- define "fusion-wizard.tokenVolumes" -}}
{{- range $name := list "forge" "index" "weave" }}
{{- $up := index $.Values.upstreams $name }}
{{- if $up.auth.enabled }}
- name: {{ $name }}-token
  projected:
    sources:
      - serviceAccountToken:
          path: token
          expirationSeconds: 3600
          {{- if $up.auth.audience }}
          audience: {{ $up.auth.audience | quote }}
          {{- end }}
{{- end }}
{{- end }}
{{- end }}

{{- define "fusion-wizard.tokenMounts" -}}
{{- range $name := list "forge" "index" "weave" }}
{{- $up := index $.Values.upstreams $name }}
{{- if $up.auth.enabled }}
- name: {{ $name }}-token
  mountPath: /var/run/secrets/wizard/{{ $name }}
  readOnly: true
{{- end }}
{{- end }}
{{- end }}

{{- define "fusion-wizard.tokenEnv" -}}
{{- range $name := list "forge" "index" "weave" }}
{{- $up := index $.Values.upstreams $name }}
{{- if $up.auth.enabled }}
- name: {{ upper $name }}_TOKEN_PATH
  value: /var/run/secrets/wizard/{{ $name }}/token
{{- end }}
{{- end }}
{{- end }}
