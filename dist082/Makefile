# Makefile para dist082 (cliente2 y partida2)

all: docker-jugador2-servidor2


build-cliente2:
	sudo docker build -t cliente2 ./cliente


build-partida2:
	sudo docker build -t partida2 ./partidas




docker-jugador2-servidor2: build-cliente2 build-partida2
	-sudo docker rm -f cliente2 || true
	-sudo docker rm -f partida2 || true
	sudo docker run -d --rm -p 50053:50053 --name partida2 \
		-e PARTIDA_ID=Partida-2 \
		-e PARTIDA_PORT=50053 \
		-e MATCHMAKER_ADDRESS=10.35.168.94:50051 \
		-e LOG_PREFIX="[Partida-2]" \
		partida2
	sudo docker run --rm -it --name cliente2 \
		-e CLIENTE_ID=Cliente2 \
		-e MATCHMAKER_ADDRESS=10.35.168.94:50051 \
		cliente2


clean:
	-sudo docker rm -f cliente2 || true
	-sudo docker rm -f partida2 || true
	sudo docker rmi -f cliente2 partida2


logs-partida:
	sudo docker logs -f partida2