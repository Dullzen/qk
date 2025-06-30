# Makefile para matchmaker

all: run

build:
	sudo docker build -t matchmaker .

run: build
	-sudo docker rm -f matchmaker || true
	sudo docker run --rm -it -p 50053:50053 --name matchmaker matchmaker

clean:
	sudo docker rmi -f matchmaker