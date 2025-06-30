# Makefile para dist084 (clienteadmin y matchmaker)

all: docker-admin-matchmaker


build-clienteadmin:
	sudo docker build -t clienteadmin ./clienteadmin


build-matchmaker:
	sudo docker build -t matchmaker ./matchmaker



docker-admin-matchmaker: build-clienteadmin build-matchmaker
	-sudo docker rm -f clienteadmin || true
	-sudo docker rm -f matchmaker || true
	# Run matchmaker first (exposing the port)
	sudo docker run -d --rm -p 50051:50051 --name matchmaker \
		-e LOG_PREFIX="[Matchmaker]" \
		matchmaker
	# Run clienteadmin with interactive mode
	sudo docker run --rm -it --name clienteadmin \
		-e MATCHMAKER_ADDRESS=localhost:50051 \
		clienteadmin


clean:
	-sudo docker rm -f clienteadmin || true
	-sudo docker rm -f matchmaker || true
	sudo docker rmi -f clienteadmin matchmaker


logs-matchmaker:
	sudo docker logs -f matchmaker