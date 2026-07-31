{{- define "mem.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mem.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "mem.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "mem.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mem.labels" -}}
helm.sh/chart: {{ include "mem.chart" . }}
app.kubernetes.io/name: {{ include "mem.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "mem.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mem.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "mem.componentLabels" -}}
{{ include "mem.selectorLabels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "mem.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "mem.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "mem.image" -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}

{{- define "mem.topologySpread" -}}
{{- if .values.enabled }}
- maxSkew: {{ .values.maxSkew }}
  topologyKey: {{ .values.topologyKey | quote }}
  whenUnsatisfiable: {{ .values.whenUnsatisfiable }}
  labelSelector:
    matchLabels:
      {{- include "mem.componentLabels" (dict "root" .root "component" .component) | nindent 6 }}
{{- end }}
{{- end -}}
