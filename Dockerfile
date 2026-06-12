FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lurker . \
    && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lurker /lurker
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV LURKER_DB_PATH=/data/lurker.db
VOLUME /data
EXPOSE 3000
ENTRYPOINT ["/lurker"]
