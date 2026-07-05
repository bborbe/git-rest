{{/* Common labels applied to every object. */}}
{{- define "git-rest.labels" -}}
app.kubernetes.io/name: git-rest
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- with .Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
{{- end -}}

{{/* Image ref: registry/repository:tag, tag defaulting to appVersion. */}}
{{- define "git-rest.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $tag -}}
{{- end -}}

{{/*
Per-vault instance name: vault-obsidian-<name>.
Call with a dict: {{ include "git-rest.vaultName" (dict "name" $vault.name) }}
*/}}
{{- define "git-rest.vaultName" -}}
{{- printf "vault-obsidian-%s" .name -}}
{{- end -}}

{{/*
Secret name a vault's StatefulSet references: existingSecret if set, else the
chart-created secret (named the same as the instance).
Call with a dict: {{ include "git-rest.vaultSecretName" (dict "vault" $vault) }}
*/}}
{{- define "git-rest.vaultSecretName" -}}
{{- .vault.existingSecret | default (printf "vault-obsidian-%s" .vault.name) -}}
{{- end -}}
