# Makefile para dist081 (cliente1 y partida1)

all: docker-jugador1-servidor1


build-cliente1:
	sudo docker build -t cliente1 ./cliente


build-partida1:
	sudo docker build -t partida1 ./partidas



docker-jugador1-servidor1: build-cliente1 build-partida1
	-sudo docker rm -f cliente1 || true
	-sudo docker rm -f partida1 || true
	sudo docker run -d --rm -p 50052:50052 --name partida1 \
		-e PARTIDA_ID=Partida-1 \
		-e PARTIDA_PORT=50052 \
		-e MATCHMAKER_ADDRESS=10.35.168.94:50051 \
		-e LOG_PREFIX="[Partida-1]" \
		partida1
	sudo docker run --rm -it --name cliente1 \
		-e CLIENTE_ID=Cliente1 \
		-e MATCHMAKER_ADDRESS=10.35.168.94:50051 \
		cliente1


clean:
	-sudo docker rm -f cliente1 || true
	-sudo docker rm -f partida1 || true
	sudo docker rmi -f cliente1 partida1


logs-partida:
	sudo docker logs -f partida1