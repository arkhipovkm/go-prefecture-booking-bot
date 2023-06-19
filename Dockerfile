FROM golang:1.19-alpine3.18

WORKDIR /var/lib/app

COPY . .

RUN go build -o app .

CMD ["./app"]