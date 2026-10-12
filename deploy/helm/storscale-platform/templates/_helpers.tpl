{{/* Every pod has enableServiceLinks: false. Kubernetes would otherwise give
     each pod a variable per Service, such as SUPERSET_PORT=tcp://10.96.0.1:8088,
     and some tools read those names as their own settings. The platform
     reaches its services by name. */}}

{{/* An image built from this repository. */}}
{{- define "storscale.image" -}}
{{- $root := index . 0 -}}{{- $name := index . 1 -}}
{{ $root.Values.images.registry }}/{{ index $root.Values.images $name }}:{{ $root.Values.images.tag | default $root.Chart.AppVersion }}
{{- end }}

{{/* The platform's address, and its tools' addresses: the port is left out
     when it's the scheme's own (443 for https, 80 for http). */}}
{{- define "storscale.portSuffix" -}}
{{- $port := toString .Values.global.port -}}
{{- if not (has (list .Values.global.scheme $port) (list (list "https" "443") (list "http" "80") (list "https" "") (list "http" ""))) }}:{{ $port }}{{ end }}
{{- end }}
{{- define "storscale.url" -}}
{{ .Values.global.scheme }}://{{ .Values.global.domain }}{{ include "storscale.portSuffix" . }}
{{- end }}
{{- define "storscale.subURL" -}}
{{- $root := index . 0 -}}{{ $root.Values.global.scheme }}://{{ index . 1 }}.{{ $root.Values.global.domain }}{{ include "storscale.portSuffix" $root }}
{{- end }}

{{- define "storscale.labels" -}}
app.kubernetes.io/part-of: storscale-platform
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "storscale.selector" -}}
app.kubernetes.io/instance: {{ index . 0 }}
app.kubernetes.io/name: {{ index . 1 }}
{{- end }}

{{/* What Compose's env_file (.env) and x-urls give a service. */}}
{{- define "storscale.envFrom" -}}
envFrom:
  - secretRef: {name: storscale-env}
  - configMapRef: {name: storscale-config}
{{- end }}

{{/* A directory of the stack's files, as a ConfigMap: ConfigMaps hold no
     directories, so a file's path is its key with "/" as "__", and the
     volume (storscale.filesVolume) puts each file back at its path. */}}
{{- define "storscale.filesConfigMap" -}}
{{- $root := index . 0 -}}{{- $name := index . 1 -}}{{- $dir := index . 2 -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: files-{{ $name }}
  labels: {{- include "storscale.labels" $root | nindent 4 }}
data:
{{- range $path, $_ := $root.Files.Glob (printf "files/%s/**" $dir) }}
{{- $rel := trimPrefix (printf "files/%s/" $dir) $path }}
{{- if not (or (contains "__pycache__" $rel) (hasSuffix ".pyc" $rel)) }}
  {{ $rel | replace "/" "__" }}: {{ $root.Files.Get $path | quote }}
{{- end }}
{{- end }}
{{- end }}

{{- define "storscale.filesVolume" -}}
{{- $root := index . 0 -}}{{- $name := index . 1 -}}{{- $dir := index . 2 -}}
- name: files-{{ $name }}
  configMap:
    name: files-{{ $name }}
    items:
{{- range $path, $_ := $root.Files.Glob (printf "files/%s/**" $dir) }}
{{- $rel := trimPrefix (printf "files/%s/" $dir) $path }}
{{- if not (or (contains "__pycache__" $rel) (hasSuffix ".pyc" $rel)) }}
      - {key: {{ $rel | replace "/" "__" | quote }}, path: {{ $rel | quote }}}
{{- end }}
{{- end }}
{{- end }}

{{/* Wait, before a service starts, as Compose's depends_on does:
       url <url>             until it answers 2xx
       job <name>            until the setup step <name> of this release has finished
       secret <name> <key>   until a Secret has the key
     (stack/tools/wait.py) */}}
{{- define "storscale.wait" -}}
{{- $root := index . 0 -}}{{- $name := index . 1 -}}{{- $args := index . 2 -}}
- name: wait-{{ $name }}
  image: {{ include "storscale.image" (list $root "tools") }}
  imagePullPolicy: {{ $root.Values.images.pullPolicy }}
  command: ["python", "-u", "/tools/wait.py"]
  args: {{ toJson $args }}
  env:
    - {name: STORSCALE_REVISION, value: {{ $root.Release.Revision | quote }}}
  volumeMounts:
    - {name: files-tools, mountPath: /tools}
  resources:
    requests: {cpu: 10m, memory: 32Mi}
{{- end }}

{{/* Trusting a private CA (global.ca). An init container (ca-bundle) adds
     the CAs to the public ones from the tools image, as a PEM bundle and as
     a truststore for Java, in /etc/storscale/ca; the environment points
     each language's TLS at them. Each renders nothing without global.ca.
       storscale.ca.init     in initContainers, before the rest
       storscale.ca.env      in a container's env
       storscale.ca.mount    in a container's volumeMounts
       storscale.ca.volumes  in the pod's volumes */}}
{{- define "storscale.ca.enabled" -}}
{{- if and .Values.global.ca.certs .Values.global.ca.configMap }}
{{- fail "global.ca: give certs or configMap, not both" }}
{{- end }}
{{- if or .Values.global.ca.certs .Values.global.ca.configMap }}true{{ end }}
{{- end }}

{{/* The chart's ConfigMap is named after its content, so pods that use it
     start afresh, with the new bundle, when the CAs change. */}}
{{- define "storscale.ca.configMap" -}}
{{- .Values.global.ca.configMap | default (printf "storscale-ca-%s" (.Values.global.ca.certs | sha256sum | trunc 8)) }}
{{- end }}

{{- define "storscale.ca.init" -}}
{{- if include "storscale.ca.enabled" . -}}
- name: ca-bundle
  image: {{ include "storscale.image" (list . "tools") }}
  imagePullPolicy: {{ .Values.images.pullPolicy }}
  command: ["sh", "-c"]
  args:
    - cat /etc/ssl/certs/ca-certificates.crt /ca-in/ca.crt > /ca/ca-certificates.crt &&
      cp /ca-in/ca.crt /ca/extra.crt &&
      openssl pkcs12 -export -nokeys -jdktrust anyExtendedKeyUsage -in /ca/ca-certificates.crt
        -out /ca/truststore.p12 -passout pass:changeit &&
      chmod 644 /ca/*
  volumeMounts:
    - {name: storscale-ca-in, mountPath: /ca-in}
    - {name: storscale-ca, mountPath: /ca}
  resources:
    requests: {cpu: 10m, memory: 32Mi}
{{- end }}
{{- end }}

{{/* Go and OpenSSL read SSL_CERT_FILE, Python's requests REQUESTS_CA_BUNDLE,
     boto3 AWS_CA_BUNDLE, Node NODE_EXTRA_CA_CERTS, and Java its default
     truststore. */}}
{{- define "storscale.ca.env" -}}
{{- if include "storscale.ca.enabled" . -}}
- {name: SSL_CERT_FILE, value: /etc/storscale/ca/ca-certificates.crt}
- {name: REQUESTS_CA_BUNDLE, value: /etc/storscale/ca/ca-certificates.crt}
- {name: CURL_CA_BUNDLE, value: /etc/storscale/ca/ca-certificates.crt}
- {name: AWS_CA_BUNDLE, value: /etc/storscale/ca/ca-certificates.crt}
- {name: NODE_EXTRA_CA_CERTS, value: /etc/storscale/ca/extra.crt}
- {name: JAVA_TOOL_OPTIONS, value: "-Djavax.net.ssl.trustStore=/etc/storscale/ca/truststore.p12 -Djavax.net.ssl.trustStoreType=PKCS12 -Djavax.net.ssl.trustStorePassword=changeit"}
{{- end }}
{{- end }}

{{- define "storscale.ca.mount" -}}
{{- if include "storscale.ca.enabled" . -}}
- {name: storscale-ca, mountPath: /etc/storscale/ca, readOnly: true}
{{- end }}
{{- end }}

{{- define "storscale.ca.volumes" -}}
{{- if include "storscale.ca.enabled" . -}}
- name: storscale-ca-in
  configMap:
    name: {{ include "storscale.ca.configMap" . }}
    items: [{key: {{ .Values.global.ca.key | quote }}, path: ca.crt}]
- {name: storscale-ca, emptyDir: {}}
{{- end }}
{{- end }}

{{/* Resources: what a service asks for, and the most memory it may use. */}}
{{- define "storscale.resources" -}}
resources:
  requests: {cpu: {{ index . 0 }}, memory: {{ index . 1 }}}
  limits: {memory: {{ index . 2 }}}
{{- end }}
