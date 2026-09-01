FROM registry.access.redhat.com/ubi9/go-toolset:1.26.7-1788245275 AS builder
WORKDIR /opt/app-root/src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o /opt/app-root/src/kube-applier-gcp .

FROM registry.access.redhat.com/ubi9/ubi-micro:9.8-1787778798

LABEL name="kube-applier-gcp" \
      com.redhat.component="kube-applier-gcp" \
      version="0.0.1" \
      release="1" \
      description="Kubernetes manifest applier for GCP HCP, reconciling Firestore Desire documents against Kubernetes." \
      io.k8s.description="Kubernetes manifest applier for GCP HCP, reconciling Firestore Desire documents against Kubernetes." \
      summary="kube-applier-gcp" \
      distribution-scope="private" \
      url="https://github.com/openshift-online/kube-applier-gcp" \
      vendor="Red Hat, Inc."

RUN mkdir /app && chown 65532:65532 /app
COPY --from=builder /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /opt/app-root/src/kube-applier-gcp /app/kube-applier-gcp
USER 65532:65532
WORKDIR /app
ENTRYPOINT ["/app/kube-applier-gcp"]
