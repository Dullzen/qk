FROM golang:1.24.2

WORKDIR /app

COPY . .

RUN go mod download

COPY . .
RUN go build -o partidas .

EXPOSE 50052-50054

CMD ["./partidas"]