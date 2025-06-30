# Makefile para clienteadmin

all: run

build:
	sudo docker build -t clienteadmin .

run: build
	-sudo docker rm -f clienteadmin || true
	sudo docker run --rm -it -p 50057:50057 --name clienteadmin clienteadmin

clean:
	sudo docker rmi -f clienteadmin