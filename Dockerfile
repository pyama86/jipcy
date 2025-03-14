FROM golang:latest as builder
ADD . /opt/jipcy
WORKDIR /opt/jipcy/
ENV CGO_ENABLED=0
RUN GOOS=linux make build

FROM scratch
COPY --from=builder /opt/jipcy/bin/jipcy /bin/jipcy
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
CMD ["/bin/jipcy"]
