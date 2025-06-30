# Makefile para dist083 (partida3)

all: docker-servidor3


build-partida3:
	sudo docker build -t partida3 ./partidas


docker-servidor3: build-partida3
	-sudo docker rm -f partida3 || true
	# Run partida3 with environment variables
	sudo docker run --rm -p 50054:50054 --name partida3 \
		-e PARTIDA_ID=Partida-3 \
		-e PARTIDA_PORT=50054 \
		-e MATCHMAKER_ADDRESS=10.35.168.94:50051 \
		partida3


clean:
	-sudo docker rm -f partida3 || true
	sudo docker rmi -f partida3