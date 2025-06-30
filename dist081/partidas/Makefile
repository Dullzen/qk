# Makefile para partidas

all: run

build:
	sudo docker build -t matchmaker .

run: build
	-sudo docker rm -f partidas || true
	sudo docker run --rm -it -p 50053:50053 --name partidas matchmaker

clean:
	sudo docker rmi -f partidas
