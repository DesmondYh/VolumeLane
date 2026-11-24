FROM golang:1.26-alpine AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/device-files . \
    && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o /out/device-files-arm64 . \
    && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/device-files-amd64 . \
    && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/device-files-windows-amd64.exe .

FROM alpine:3.24
WORKDIR /app
COPY --from=build /out/device-files /app/device-files
COPY --from=build /out/device-files-arm64 /out/device-files-amd64 /out/device-files-windows-amd64.exe /app/install/
COPY install/Install.command install/Install.bat install/Install.ps1 /app/install/
COPY web/ /app/web/
CMD ["/app/device-files", "hub", "/state/hub.json", "0.0.0.0:8080"]
