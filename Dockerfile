FROM        quay.io/prometheus/busybox:latest
MAINTAINER  Samuel BERTHE <samuel.berthe@iadvize.com>

COPY nomad_exporter /bin/nomad_exporter

EXPOSE     9000
ENTRYPOINT [ "/bin/nomad_exporter" ]
