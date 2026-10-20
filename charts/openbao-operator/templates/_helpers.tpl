{{/*
Expand the name of the chart.
*/}}
{{- define "openbao-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "openbao-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "openbao-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Operator version (for status reporting)
*/}}
{{- define "openbao-operator.operatorVersion" -}}
{{- if .Values.operatorVersion -}}
{{- .Values.operatorVersion -}}
{{- else -}}
{{- .Chart.AppVersion -}}
{{- end -}}
{{- end -}}

{{/*
Manager image reference
*/}}
{{- define "openbao-operator.managerImage" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "openbao-operator.labels" -}}
helm.sh/chart: {{ include "openbao-operator.chart" . }}
app.kubernetes.io/name: {{ include "openbao-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{/*
Controller selector labels
*/}}
{{- define "openbao-operator.controllerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "openbao-operator.name" . }}
app.kubernetes.io/component: controller
{{- end -}}

{{/*
Provisioner selector labels
*/}}
{{- define "openbao-operator.provisionerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "openbao-operator.name" . }}
app.kubernetes.io/component: provisioner
{{- end -}}

{{/*
Controller service account name
*/}}
{{- define "openbao-operator.controllerServiceAccountName" -}}
{{- printf "%s-controller" (include "openbao-operator.fullname" .) -}}
{{- end -}}

{{/*
Provisioner service account name
*/}}
{{- define "openbao-operator.provisionerServiceAccountName" -}}
{{- printf "%s-provisioner" (include "openbao-operator.fullname" .) -}}
{{- end -}}

{{/*
Provisioner RBAC ValidatingAdmissionPolicy: deny system namespaces.

Returns a CEL boolean expression snippet like:
  request.namespace == "ns" || request.namespace.startsWith("kube-") || ...

The release namespace is always included. Users may extend the deny set via:
  admissionPolicies.provisionerRBAC.deniedNamespaces
  admissionPolicies.provisionerRBAC.deniedNamespacePrefixes
*/}}
{{- define "openbao-operator.admission.provisionerRBACSystemNamespaceClauses" -}}
{{- $cfg := default dict .Values.admissionPolicies.provisionerRBAC -}}
{{- $clauses := list (printf "request.namespace == %q" .Release.Namespace) -}}
{{- range $ns := (default (list) $cfg.deniedNamespaces) -}}
  {{- $ns = (toString $ns | trim) -}}
  {{- if $ns -}}
    {{- $clauses = append $clauses (printf "request.namespace == %q" $ns) -}}
  {{- end -}}
{{- end -}}
{{- range $prefix := (default (list) $cfg.deniedNamespacePrefixes) -}}
  {{- $prefix = (toString $prefix | trim) -}}
  {{- if $prefix -}}
    {{- $clauses = append $clauses (printf "request.namespace.startsWith(%q)" $prefix) -}}
  {{- end -}}
{{- end -}}
{{- join " ||\n" $clauses -}}
{{- end -}}

{{/*
Provisioner Namespace mutation VAP: deny system namespaces by name.

Returns a CEL boolean expression snippet like:
  request.name == "ns" || request.name.startsWith("kube-") || ...

The release namespace is always included. Users may extend the deny set via:
  admissionPolicies.provisionerRBAC.deniedNamespaces
  admissionPolicies.provisionerRBAC.deniedNamespacePrefixes
*/}}
{{- define "openbao-operator.admission.provisionerSystemNamespaceNameClauses" -}}
{{- $cfg := default dict .Values.admissionPolicies.provisionerRBAC -}}
{{- $clauses := list (printf "request.name == %q" .Release.Namespace) -}}
{{- range $ns := (default (list) $cfg.deniedNamespaces) -}}
  {{- $ns = (toString $ns | trim) -}}
  {{- if $ns -}}
    {{- $clauses = append $clauses (printf "request.name == %q" $ns) -}}
  {{- end -}}
{{- end -}}
{{- range $prefix := (default (list) $cfg.deniedNamespacePrefixes) -}}
  {{- $prefix = (toString $prefix | trim) -}}
  {{- if $prefix -}}
    {{- $clauses = append $clauses (printf "request.name.startsWith(%q)" $prefix) -}}
  {{- end -}}
{{- end -}}
{{- join " ||\n" $clauses -}}
{{- end -}}

{{/* Exact virtual-API usernames mapped to their approved PVC namespaces. */}}
{{- define "openbao-operator.pvcBindingSyncCallers" -}}
{{- $callers := dict -}}
{{- range .Values.admissionPolicies.pvcBindingSync.allowedCallers -}}
{{- $namespaces := default (list) (get $callers .username) -}}
{{- $_ := set $callers .username (concat $namespaces .namespaces | uniq) -}}
{{- end -}}
{{- toJson $callers -}}
{{- end -}}
