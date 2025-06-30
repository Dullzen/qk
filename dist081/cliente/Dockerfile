FROM golang:1.24.2

WORKDIR /app

COPY . .

RUN go mod tidy
RUN go build -o cliente .

# Asegurarse de que el contenedor acepte entrada estándar
ENV TERM=xterm

CMD ["./cliente"]