# Makefile para cliente

all: run

build:
	sudo docker build -t cliente .

run: build
	-sudo docker rm -f cliente || true
	sudo docker run --rm -it -p 50051:50051 --name cliente cliente

clean:
	sudo docker rmi -f cliente