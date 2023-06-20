FROM golang:1.19-alpine3.18

WORKDIR /var/lib/app

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY main.go .

RUN go build -o app .

CMD ["./app"]