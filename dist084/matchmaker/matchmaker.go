package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	pb "matchmaker/proto/grpc-server/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Dirección del servidor
const (
	port = ":50051"
)

// Estado de la partida
type EstadoPartida string

const (
	Esperando  EstadoPartida = "Esperando"
	EnCurso    EstadoPartida = "En curso"
	Finalizada EstadoPartida = "Finalizada"
)

// Resultado de una partida
type ResultadoPartida struct {
	Ganador  string
	Perdedor string
}

// Estructura para representar una partida
type Partida struct {
	ID            string
	Clientes      []string
	Estado        EstadoPartida
	Resultado     *ResultadoPartida
	ServidorID    string    // ID del servidor que ejecuta esta partida
	InicioPartida time.Time // Timestamp de cuando inició la partida
	mutex         sync.RWMutex
}

// Verificar si la partida está llena
func (p *Partida) EstaLlena() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return len(p.Clientes) >= 2
}

// Verificar si la partida está en curso o finalizada
func (p *Partida) EstaActiva() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.Estado == EnCurso || p.Estado == Finalizada
}

// Añadir un cliente a la partida
func (p *Partida) AñadirCliente(clienteID string) bool {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Verificar si el cliente ya está en la partida
	for _, c := range p.Clientes {
		if c == clienteID {
			return false
		}
	}

	// Verificar si hay espacio y la partida está esperando jugadores
	if len(p.Clientes) >= 2 || p.Estado != Esperando {
		return false
	}

	// Añadir el cliente
	p.Clientes = append(p.Clientes, clienteID)

	// Si ahora está llena, cambiar el estado a en curso
	if len(p.Clientes) == 2 {
		p.Estado = EnCurso
	}

	return true
}

// Eliminar un cliente de la partida
func (p *Partida) EliminarCliente(clienteID string) bool {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// No permitir eliminación si la partida está activa
	if p.Estado == EnCurso {
		return false
	}

	for i, c := range p.Clientes {
		if c == clienteID {
			// Eliminar el cliente usando la técnica de reordenamiento
			p.Clientes[i] = p.Clientes[len(p.Clientes)-1]
			p.Clientes = p.Clientes[:len(p.Clientes)-1]
			return true
		}
	}
	return false
}

// Simular el resultado de la partida
func (p *Partida) SimularPartida() *ResultadoPartida {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.Estado != EnCurso || len(p.Clientes) != 2 {
		return nil
	}

	// Elegir un ganador aleatoriamente
	rand.Seed(time.Now().UnixNano())
	ganadorIdx := rand.Intn(2)
	perdedorIdx := (ganadorIdx + 1) % 2

	// Crear el resultado
	p.Resultado = &ResultadoPartida{
		Ganador:  p.Clientes[ganadorIdx],
		Perdedor: p.Clientes[perdedorIdx],
	}

	// Actualizar estado
	p.Estado = Finalizada

	return p.Resultado
}

// Convertir a mensaje protobuf
func (p *Partida) ToProto() *pb.Partida {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	// Si esta partida está siendo ejecutada por un servidor externo,
	// indicamos esto pero no creamos una entrada duplicada
	partidaProto := &pb.Partida{
		Id:         p.ID,
		Clientes:   append([]string{}, p.Clientes...), // Crear una copia
		Llena:      len(p.Clientes) >= 2,
		Estado:     string(p.Estado),
		ServidorId: p.ServidorID, // Añadir el ID del servidor a la respuesta
	}

	if p.Resultado != nil {
		partidaProto.Ganador = p.Resultado.Ganador
		partidaProto.Perdedor = p.Resultado.Perdedor
	}

	return partidaProto
}

// Estructura para manejar el reloj vectorial
type VectorClock struct {
	clock map[string]int32
	mutex sync.RWMutex
}

// Crear un nuevo reloj vectorial
func NewVectorClock(id string) *VectorClock {
	vc := &VectorClock{
		clock: make(map[string]int32),
	}
	vc.clock[id] = 0
	return vc
}

// Incrementar el contador propio
func (vc *VectorClock) Increment(id string) {
	vc.mutex.Lock()
	defer vc.mutex.Unlock()
	vc.clock[id]++
}

// Obtener una copia del reloj actual
func (vc *VectorClock) Get() map[string]int32 {
	vc.mutex.RLock()
	defer vc.mutex.RUnlock()

	// Crear una copia para evitar problemas de concurrencia
	copy := make(map[string]int32)
	for k, v := range vc.clock {
		copy[k] = v
	}
	return copy
}

// Actualizar el reloj vectorial con otro reloj
func (vc *VectorClock) Update(other map[string]int32) {
	vc.mutex.Lock()
	defer vc.mutex.Unlock()

	// Incorporar todos los valores del otro reloj
	for id, value := range other {
		// Si no existe o el otro tiene un valor mayor, actualizamos
		if currentValue, exists := vc.clock[id]; !exists || value > currentValue {
			vc.clock[id] = value
		}
	}
}

// Servidor implementando la interfaz definida en proto
type server struct {
	pb.UnimplementedMatchmakerServer
	pb.UnimplementedMatchmakerServiceServer
	pb.UnimplementedAdminServiceServer // Add this line
	vectorClock                        *VectorClock
	partidas                           map[string]*Partida
	clientePartida                     map[string]string // Mapea clientes a sus partidas o "cola"
	mutex                              sync.Mutex
	partidaMutex                       sync.RWMutex

	// Cola de emparejamiento
	colaJugadores []string
	colaMutex     sync.RWMutex

	// Registrar servidor de partidas
	servidoresPartidas map[string]*ServidorPartida // ID del servidor -> info del servidor
	servidoresMutex    sync.RWMutex
}

// Crear nuevas partidas iniciales
func (s *server) crearPartidasIniciales(numPartidas int) {
	s.partidaMutex.Lock()
	defer s.partidaMutex.Unlock()

	for i := 0; i < numPartidas; i++ {
		partidaID := fmt.Sprintf("Partida-%d", i+1)
		s.partidas[partidaID] = &Partida{
			ID:       partidaID,
			Clientes: []string{},
			Estado:   Esperando,
		}
	}
	log.Printf("Creadas %d partidas iniciales", numPartidas)
}

// Verificar si un cliente ya está inscrito en alguna partida
func (s *server) clienteYaInscrito(clienteID string) bool {
	s.partidaMutex.RLock()
	defer s.partidaMutex.RUnlock()

	_, existe := s.clientePartida[clienteID]
	return existe
}

// Obtener todas las partidas
func (s *server) obtenerTodasLasPartidas() []*Partida {
	s.partidaMutex.RLock()
	defer s.partidaMutex.RUnlock()

	// Usar un mapa para detectar duplicados (mismo conjunto de jugadores)
	vistas := make(map[string]bool)
	var todas []*Partida

	for _, p := range s.partidas {
		// Crear una clave única basada en los jugadores
		jugadoresKey := strings.Join(p.Clientes, ",")

		// Si la partida tiene jugadores y no hemos visto esta combinación antes
		if len(p.Clientes) > 0 && !vistas[jugadoresKey] {
			vistas[jugadoresKey] = true
			todas = append(todas, p)
		} else if len(p.Clientes) == 0 {
			// Partidas vacías siempre se muestran
			todas = append(todas, p)
		}
	}

	return todas
}

// Asignar un cliente a una partida aleatoria disponible
func (s *server) asignarClienteAPartida(clienteID string) (string, bool) {
	// Verificar si el cliente ya está inscrito
	if s.clienteYaInscrito(clienteID) {
		log.Printf("Cliente %s ya está inscrito en una partida", clienteID)
		return "", false
	}

	// Obtener partidas realmente disponibles (verificación adicional)
	s.partidaMutex.RLock()
	var disponibles []*Partida
	for _, p := range s.partidas {
		if !p.EstaLlena() && p.Estado == Esperando {
			disponibles = append(disponibles, p)
		}
	}
	s.partidaMutex.RUnlock()

	// Si no hay disponibles, crear nueva
	if len(disponibles) == 0 {
		// Crear una nueva partida si no hay disponibles
		s.partidaMutex.Lock()
		nuevaPartidaID := fmt.Sprintf("Partida-%d", len(s.partidas)+1)
		nuevaPartida := &Partida{
			ID:       nuevaPartidaID,
			Clientes: []string{},
			Estado:   Esperando,
		}
		s.partidas[nuevaPartidaID] = nuevaPartida
		s.partidaMutex.Unlock()

		disponibles = append(disponibles, nuevaPartida)
	}

	// Seleccionar una partida aleatoria de las disponibles
	rand.Seed(time.Now().UnixNano())
	partidaElegida := disponibles[rand.Intn(len(disponibles))]

	// Añadir el cliente a la partida
	if partidaElegida.AñadirCliente(clienteID) {
		s.partidaMutex.Lock()
		s.clientePartida[clienteID] = partidaElegida.ID
		s.partidaMutex.Unlock()

		// Si la partida está llena, iniciar simulación después de un breve periodo
		if partidaElegida.EstaLlena() {
			// Reemplazar esta línea:
			// go s.simularPartidaDespuesDe(partidaElegida.ID, 5*time.Second)

			// Por esta lógica:
			go func(partidaID string) {
				// Buscar un servidor disponible inmediatamente
				servidor, encontrado := s.obtenerServidorPartidaDisponible()
				if !encontrado {
					log.Printf("No hay servidores disponibles para la partida %s. Usando simulación local...", partidaID)
					// Fallback a simulación local después de un breve periodo
					time.Sleep(5 * time.Second)
					s.realizarSimulacionLocal(partidaID)
					return
				}

				log.Printf("Asignando partida %s a servidor externo %s en %s",
					partidaID, servidor.ID, servidor.Address)

				// Obtener los clientes de la partida
				s.partidaMutex.RLock()
				partida, existe := s.partidas[partidaID]
				if !existe || len(partida.Clientes) != 2 {
					s.partidaMutex.RUnlock()
					return
				}
				clientes := append([]string{}, partida.Clientes...)
				s.partidaMutex.RUnlock()

				// Conectar al servidor de partidas
				conn, err := grpc.Dial(
					servidor.Address,
					grpc.WithTransportCredentials(insecure.NewCredentials()),
					grpc.WithBlock(),
					grpc.WithTimeout(3*time.Second),
				)

				// Imprimir información de debug
				log.Printf("Intentando conectar a servidor %s en dirección '%s'",
					servidor.ID, servidor.Address)

				if err != nil {
					log.Printf("Error al conectar con servidor %s: %v", servidor.ID, err)
					s.servidoresMutex.Lock()
					s.servidoresPartidas[servidor.ID].Status = "CAIDO"
					s.servidoresMutex.Unlock()

					// Fallback a simulación local
					time.Sleep(2 * time.Second)
					s.realizarSimulacionLocal(partidaID)
					return
				}
				defer conn.Close()

				// Crear cliente gRPC y enviar solicitud
				client := pb.NewPartidaServiceClient(conn)

				// Incrementar el reloj vectorial
				s.mutex.Lock()
				s.vectorClock.Increment("Matchmaker")
				respClock := s.vectorClock.Get()
				s.mutex.Unlock()

				// Crear solicitud
				req := &pb.AssignMatchRequest{
					MatchId:        partidaID,
					PlayerIds:      clientes,
					RelojVectorial: respClock,
				}

				// Enviar solicitud con timeout
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				resp, err := client.AssignMatch(ctx, req)
				if err != nil {
					log.Printf("ERROR: No se pudo asignar partida %s a servidor %s: %v",
						partidaID, servidor.ID, err)

					// Fallback a simulación local
					time.Sleep(2 * time.Second)
					s.realizarSimulacionLocal(partidaID)
					return
				}

				// Actualizar reloj vectorial con la respuesta
				s.mutex.Lock()
				s.vectorClock.Update(resp.GetRelojVectorial())
				s.mutex.Unlock()

				// Al enviar AssignMatchRequest y recibir respuesta exitosa, actualizar el campo ServidorID
				if resp.StatusCode == 0 {
					s.partidaMutex.Lock()
					if partida, existe := s.partidas[partidaID]; existe {
						partida.ServidorID = servidor.ID
						log.Printf("Partida %s asignada y vinculada al servidor %s",
							partidaID, servidor.ID)
					}
					s.partidaMutex.Unlock()
				}

				log.Printf("Partida %s asignada correctamente a servidor %s (código: %d, mensaje: %s)",
					partidaID, servidor.ID, resp.GetStatusCode(), resp.GetMessage())

			}(partidaElegida.ID)
		}

		return partidaElegida.ID, true
	}

	return "", false
}

// Asignar un cliente a la cola de emparejamiento
func (s *server) asignarClienteACola(clienteID string) bool {
	s.partidaMutex.Lock()
	defer s.partidaMutex.Unlock()

	// Verificar si el cliente ya está inscrito (con el lock activo)
	if _, existe := s.clientePartida[clienteID]; existe {
		log.Printf("Cliente %s ya está inscrito en una partida o en cola", clienteID)
		return false
	}

	// Añadir a la cola de espera con un único lock
	s.colaMutex.Lock()
	s.colaJugadores = append(s.colaJugadores, clienteID)
	s.colaMutex.Unlock()

	// Registrar que el cliente está en cola (lock ya adquirido)
	s.clientePartida[clienteID] = "cola"

	log.Printf("Cliente %s añadido a la cola de emparejamiento", clienteID)

	// Intentar crear emparejamientos inmediatamente
	go s.procesarEmparejamientos()

	return true
}

// Procesar emparejamientos según la cola actual
func (s *server) procesarEmparejamientos() {
	s.colaMutex.Lock()

	// Verificar si hay suficientes jugadores
	if len(s.colaJugadores) < 2 {
		s.colaMutex.Unlock()
		log.Printf("No hay suficientes jugadores en cola para formar una partida")
		return
	}

	// Buscar un servidor disponible
	s.colaMutex.Unlock() // Desbloquear mientras buscamos servidor para no bloquear otras operaciones
	servidor, encontrado := s.obtenerServidorPartidaDisponible()

	if !encontrado {
		log.Printf("No hay servidores disponibles para crear una partida")
		return
	}

	// Tomar los primeros dos jugadores de la cola
	s.colaMutex.Lock()
	if len(s.colaJugadores) < 2 {
		// Verificar nuevamente por si alguien canceló mientras buscábamos servidor
		s.colaMutex.Unlock()
		return
	}

	jugador1 := s.colaJugadores[0]
	jugador2 := s.colaJugadores[1]

	// Remover estos jugadores de la cola
	s.colaJugadores = s.colaJugadores[2:]
	s.colaMutex.Unlock()

	// Generar un ID para la partida
	partidaID := fmt.Sprintf("Partida-%d", time.Now().UnixNano()%1000)

	// Crear la partida y asignarla
	s.partidaMutex.Lock()

	// Verificar que los jugadores sigan disponibles
	if _, ok := s.clientePartida[jugador1]; ok && s.clientePartida[jugador1] == "cola" {
		if _, ok := s.clientePartida[jugador2]; ok && s.clientePartida[jugador2] == "cola" {
			// Ambos jugadores están disponibles, crear la partida
			nuevaPartida := &Partida{
				ID:            partidaID,
				Clientes:      []string{jugador1, jugador2},
				Estado:        EnCurso,
				InicioPartida: time.Now(), // Registrar el tiempo de inicio
			}

			s.partidas[partidaID] = nuevaPartida
			s.clientePartida[jugador1] = partidaID
			s.clientePartida[jugador2] = partidaID

			log.Printf("Creada partida %s con jugadores %s y %s", partidaID, jugador1, jugador2)

			s.partidaMutex.Unlock()

			// Asignar la partida al servidor
			go s.asignarPartidaAServidor(partidaID, servidor)

			return
		}
	}

	// Devolver los jugadores disponibles a la cola
	s.colaMutex.Lock()
	if _, ok := s.clientePartida[jugador1]; ok && s.clientePartida[jugador1] == "cola" {
		s.colaJugadores = append(s.colaJugadores, jugador1)
	}
	if _, ok := s.clientePartida[jugador2]; ok && s.clientePartida[jugador2] == "cola" {
		s.colaJugadores = append(s.colaJugadores, jugador2)
	}
	s.colaMutex.Unlock()

	s.partidaMutex.Unlock()
}

// Asignar una partida a un servidor específico
func (s *server) asignarPartidaAServidor(partidaID string, servidor *ServidorPartida) {
	// Obtener los detalles de la partida
	s.partidaMutex.RLock()
	partida, existe := s.partidas[partidaID]
	if !existe || len(partida.Clientes) != 2 {
		s.partidaMutex.RUnlock()
		log.Printf("Error: La partida %s no existe o no tiene 2 jugadores", partidaID)
		return
	}
	clientes := append([]string{}, partida.Clientes...)
	s.partidaMutex.RUnlock()

	log.Printf("Asignando partida %s a servidor externo %s en %s",
		partidaID, servidor.ID, servidor.Address)

	// Conectar al servidor de partidas
	conn, err := grpc.Dial(
		servidor.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(3*time.Second),
	)

	if err != nil {
		log.Printf("Error al conectar con servidor %s: %v", servidor.ID, err)
		s.servidoresMutex.Lock()
		s.servidoresPartidas[servidor.ID].Status = "CAIDO"
		s.servidoresMutex.Unlock()

		// Fallback a simulación local
		time.Sleep(2 * time.Second)
		s.realizarSimulacionLocal(partidaID)
		return
	}
	defer conn.Close()

	// Crear cliente gRPC y enviar solicitud
	client := pb.NewPartidaServiceClient(conn)

	// Incrementar el reloj vectorial
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Crear solicitud
	req := &pb.AssignMatchRequest{
		MatchId:        partidaID,
		PlayerIds:      clientes,
		RelojVectorial: respClock,
	}

	// Enviar solicitud con timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.AssignMatch(ctx, req)
	if err != nil {
		log.Printf("ERROR: No se pudo asignar partida %s a servidor %s: %v",
			partidaID, servidor.ID, err)

		// Fallback a simulación local
		time.Sleep(2 * time.Second)
		s.realizarSimulacionLocal(partidaID)
		return
	}

	// Actualizar reloj vectorial con la respuesta
	s.mutex.Lock()
	s.vectorClock.Update(resp.GetRelojVectorial())
	s.mutex.Unlock()

	// Actualizar el campo ServidorID si la respuesta es exitosa
	if resp.StatusCode == 0 {
		s.partidaMutex.Lock()
		if partida, existe := s.partidas[partidaID]; existe {
			partida.ServidorID = servidor.ID
			log.Printf("Partida %s asignada y vinculada al servidor %s",
				partidaID, servidor.ID)
		}
		s.partidaMutex.Unlock()
	}

	log.Printf("Partida %s asignada correctamente a servidor %s (código: %d, mensaje: %s)",
		partidaID, servidor.ID, resp.GetStatusCode(), resp.GetMessage())
}

// Función para obtener un servidor de partidas disponible
func (s *server) obtenerServidorPartidaDisponible() (*ServidorPartida, bool) {
	s.servidoresMutex.RLock()
	defer s.servidoresMutex.RUnlock()

	log.Printf("Buscando servidor de partidas disponible. Servidores registrados: %d", len(s.servidoresPartidas))

	// Si no hay servidores registrados, retornar inmediatamente
	if len(s.servidoresPartidas) == 0 {
		return nil, false
	}

	// Coleccionar todos los servidores disponibles
	var disponibles []*ServidorPartida
	for id, sp := range s.servidoresPartidas {
		log.Printf("Servidor %s tiene estado: %s", id, sp.Status)
		if sp.Status == "DISPONIBLE" {
			disponibles = append(disponibles, sp)
		}
	}

	// Si no hay servidores disponibles, retornar
	if len(disponibles) == 0 {
		log.Printf("No se encontró ningún servidor de partidas disponible")
		return nil, false
	}

	// Seleccionar un servidor disponible al azar
	rand.Seed(time.Now().UnixNano())
	servidorSeleccionado := disponibles[rand.Intn(len(disponibles))]

	log.Printf("Usando servidor disponible (selección aleatoria): %s en %s",
		servidorSeleccionado.ID, servidorSeleccionado.Address)
	return servidorSeleccionado, true
}

// Función auxiliar para realizar simulación local
func (s *server) realizarSimulacionLocal(partidaID string) {
	s.partidaMutex.Lock()
	partida, existe := s.partidas[partidaID]

	if !existe || partida.Estado != EnCurso {
		s.partidaMutex.Unlock()
		return
	}

	log.Printf("Iniciando simulación local para partida %s", partidaID)
	resultado := partida.SimularPartida()

	if resultado != nil {
		log.Printf("Partida %s finalizada localmente. Ganador: %s, Perdedor: %s",
			partidaID, resultado.Ganador, resultado.Perdedor)

		// Eliminar a los jugadores de la partida del mapa clientePartida
		for _, cliente := range partida.Clientes {
			delete(s.clientePartida, cliente)
		}
	}

	s.partidaMutex.Unlock()
}

// Eliminar un cliente de su partida
func (s *server) eliminarClienteDePartida(clienteID string) bool {
	s.partidaMutex.RLock()
	partidaID, existe := s.clientePartida[clienteID]
	s.partidaMutex.RUnlock()

	if !existe {
		return false
	}

	s.partidaMutex.Lock()
	partida, existePartida := s.partidas[partidaID]
	s.partidaMutex.Unlock()

	if !existePartida {
		return false
	}

	if partida.EliminarCliente(clienteID) {
		s.partidaMutex.Lock()
		delete(s.clientePartida, clienteID)
		s.partidaMutex.Unlock()
		return true
	}

	return false
}

// Obtener la partida de un cliente
func (s *server) obtenerPartidaDeCliente(clienteID string) (*Partida, bool) {
	s.partidaMutex.RLock()
	defer s.partidaMutex.RUnlock()

	partidaID, existe := s.clientePartida[clienteID]
	if !existe {
		return nil, false
	}

	partida, existePartida := s.partidas[partidaID]
	return partida, existePartida
}

// Implementación del método Conectar
func (s *server) Conectar(ctx context.Context, req *pb.ConexionRequest) (*pb.ConexionResponse, error) {
	mensaje := req.GetMensaje()
	log.Printf("Recibida solicitud: %v", mensaje)

	// Extraer el ID del cliente del mensaje (formato: "ACCIÓN: ClienteID ...")
	partes := strings.Split(mensaje, ":")
	if len(partes) < 2 {
		return nil, fmt.Errorf("formato de mensaje incorrecto")
	}

	accion := strings.TrimSpace(partes[0])
	restoDeMensaje := strings.TrimSpace(partes[1])

	// Extraer el ID del cliente
	clienteID := strings.Split(restoDeMensaje, " ")[0]

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Preparar respuesta base
	respuesta := &pb.ConexionResponse{
		RelojVectorial: respClock,
		Exito:          true,
	}

	// Procesar según la acción
	switch accion {
	case "INSCRIPCIÓN":
		// Verificar si ya está inscrito
		if s.clienteYaInscrito(clienteID) {
			respuesta.Mensaje = "Ya estás inscrito en una partida. Cancela tu inscripción actual antes de inscribirte nuevamente."
			respuesta.Exito = false
		} else {
			// Intentar asignar a una partida
			partidaID, exito := s.asignarClienteAPartida(clienteID)
			if exito {
				respuesta.Mensaje = fmt.Sprintf("Te has inscrito exitosamente en la partida %s", partidaID)
				respuesta.PartidaId = partidaID
			} else {
				respuesta.Mensaje = "No fue posible inscribirte en una partida"
				respuesta.Exito = false
			}
		}

	case "ESTADO":
		// Convertir todas las partidas a formato protobuf
		var partidasProto []*pb.Partida
		todasPartidas := s.obtenerTodasLasPartidas()
		for _, p := range todasPartidas {
			partidasProto = append(partidasProto, p.ToProto())
		}

		// Verificar si el cliente está en alguna partida
		partida, enPartida := s.obtenerPartidaDeCliente(clienteID)
		if enPartida {
			respuesta.Mensaje = fmt.Sprintf("Estás inscrito en la partida %s (Estado: %s)", partida.ID, partida.Estado)
			respuesta.PartidaId = partida.ID

			// Si la partida finalizó, incluir el resultado
			if partida.Estado == Finalizada && partida.Resultado != nil {
				if partida.Resultado.Ganador == clienteID {
					respuesta.Mensaje = fmt.Sprintf("¡Has GANADO la partida %s contra %s!",
						partida.ID, partida.Resultado.Perdedor)
				} else {
					respuesta.Mensaje = fmt.Sprintf("Has perdido la partida %s contra %s.",
						partida.ID, partida.Resultado.Ganador)
				}
			}
		} else {
			respuesta.Mensaje = "No estás inscrito en ninguna partida"
		}

		respuesta.Partidas = partidasProto

	case "CANCELACIÓN":
		// Solo permitir cancelación si la partida no está en curso
		partida, enPartida := s.obtenerPartidaDeCliente(clienteID)
		if !enPartida {
			respuesta.Mensaje = "No estabas inscrito en ninguna partida"
			respuesta.Exito = false
		} else if partida.Estado == EnCurso {
			respuesta.Mensaje = "No puedes abandonar una partida en curso"
			respuesta.Exito = false
		} else {
			exito := s.eliminarClienteDePartida(clienteID)
			if exito {
				respuesta.Mensaje = "Has sido eliminado de la partida exitosamente"
			} else {
				respuesta.Mensaje = "No se pudo cancelar la inscripción"
				respuesta.Exito = false
			}
		}

	default:
		respuesta.Mensaje = "Acción no reconocida"
		respuesta.Exito = false
	}

	log.Printf("Respuesta: %s", respuesta.Mensaje)
	return respuesta, nil
}

// Implementación del método QueuePlayer
func (s *server) QueuePlayer(ctx context.Context, req *pb.PlayerInfoRequest) (*pb.QueuePlayerResponse, error) {
	playerID := req.GetPlayerId()
	gameMode := req.GetGameModePreference()
	log.Printf("Recibida solicitud de emparejamiento de %s para modo %s", playerID, gameMode)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Preparar respuesta base
	respuesta := &pb.QueuePlayerResponse{
		RelojVectorial: respClock,
		StatusCode:     0, // Éxito por defecto
	}

	// Si es una solicitud de cancelación
	if gameMode == "CANCEL" {
		// Verificar si el jugador está en cola
		s.partidaMutex.RLock()
		partidaID, existe := s.clientePartida[playerID]
		s.partidaMutex.RUnlock()

		if !existe {
			respuesta.StatusCode = 1 // Error
			respuesta.Message = "No estabas inscrito en ninguna cola o partida"
			return respuesta, nil
		}

		if partidaID == "cola" {
			// Eliminar de la cola
			s.colaMutex.Lock()
			for i, id := range s.colaJugadores {
				if id == playerID {
					s.colaJugadores = append(s.colaJugadores[:i], s.colaJugadores[i+1:]...)
					break
				}
			}
			s.colaMutex.Unlock()

			s.partidaMutex.Lock()
			delete(s.clientePartida, playerID)
			s.partidaMutex.Unlock()

			respuesta.Message = "Has sido eliminado de la cola exitosamente"
			return respuesta, nil
		}

		// Si ya está en una partida, usar la lógica anterior
		partida, enPartida := s.obtenerPartidaDeCliente(playerID)
		if !enPartida {
			respuesta.StatusCode = 1 // Error
			respuesta.Message = "No estabas inscrito en ninguna partida"
		} else if partida.Estado == EnCurso {
			respuesta.StatusCode = 2 // Error
			respuesta.Message = "No puedes abandonar una partida en curso"
		} else {
			exito := s.eliminarClienteDePartida(playerID)
			if exito {
				respuesta.Message = "Has sido eliminado de la partida exitosamente"
			} else {
				respuesta.StatusCode = 3 // Error
				respuesta.Message = "No se pudo cancelar la inscripción"
			}
		}
		return respuesta, nil
	}

	// Si es solicitud normal de inscripción
	// Verificar si ya está inscrito
	if s.clienteYaInscrito(playerID) {
		respuesta.StatusCode = 1 // Código de error
		respuesta.Message = "Ya estás inscrito en una partida o en cola. Cancela tu inscripción actual antes de inscribirte nuevamente."
		return respuesta, nil
	}

	// Añadir a la cola de emparejamiento
	exito := s.asignarClienteACola(playerID)
	if exito {
		respuesta.Message = "Te has inscrito exitosamente en la cola de emparejamiento"
	} else {
		respuesta.StatusCode = 2 // Otro código de error
		respuesta.Message = "No fue posible inscribirte en la cola"
	}

	log.Printf("Respuesta: %s", respuesta.Message)
	return respuesta, nil
}

// Implementación del método GetPlayerStatus
func (s *server) GetPlayerStatus(ctx context.Context, req *pb.PlayerStatusRequest) (*pb.PlayerStatusResponse, error) {
	playerID := req.GetPlayerId()
	log.Printf("Recibida solicitud de estado del jugador %s", playerID)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Convertir todas las partidas a formato protobuf
	var partidasProto []*pb.Partida
	todasPartidas := s.obtenerTodasLasPartidas()
	for _, p := range todasPartidas {
		partidasProto = append(partidasProto, p.ToProto())
	}

	// Preparar respuesta base
	respuesta := &pb.PlayerStatusResponse{
		RelojVectorial: respClock,
		PlayerStatus:   "IDLE", // Por defecto está inactivo
		Partidas:       partidasProto,
	}

	// Verificar si el jugador está en cola
	s.partidaMutex.RLock()
	partidaID, existe := s.clientePartida[playerID]
	s.partidaMutex.RUnlock()

	if existe {
		if partidaID == "cola" {
			// El jugador está en cola de espera
			respuesta.PlayerStatus = "IN_QUEUE"
			respuesta.Mensaje = "Estás en la cola de emparejamiento esperando una partida"

			// Mostrar posición en cola
			s.colaMutex.RLock()
			posicion := -1
			for i, id := range s.colaJugadores {
				if id == playerID {
					posicion = i + 1
					break
				}
			}
			s.colaMutex.RUnlock()

			if posicion > 0 {
				respuesta.Mensaje = fmt.Sprintf("Estás en la cola de emparejamiento (posición: %d)", posicion)
			}

			return respuesta, nil
		}
	}

	// Si no está en cola, verificar si está en alguna partida (lógica anterior)
	partida, enPartida := s.obtenerPartidaDeCliente(playerID)
	if enPartida {
		// Verificación adicional para evitar referencias a partidas finalizadas
		if partida.Estado == Finalizada {
			log.Printf("ADVERTENCIA: El cliente %s aparece en una partida finalizada %s. Corrigiendo...",
				playerID, partida.ID)
			// Eliminar la asociación incorrecta
			s.partidaMutex.Lock()
			delete(s.clientePartida, playerID)
			s.partidaMutex.Unlock()

			enPartida = false
			respuesta.Mensaje = "No estás inscrito en ninguna partida"
			respuesta.PlayerStatus = "IDLE"
		} else {
			// Si está en una partida activa, continuar normalmente
			switch partida.Estado {
			case Esperando:
				respuesta.PlayerStatus = "IN_QUEUE"
				respuesta.Mensaje = fmt.Sprintf("Estás inscrito en la partida %s (Estado: %s)", partida.ID, partida.Estado)
			case EnCurso:
				respuesta.PlayerStatus = "IN_MATCH"
				respuesta.Mensaje = fmt.Sprintf("Estás participando en la partida %s", partida.ID)
			}
			respuesta.PartidaId = partida.ID
		}
	} else {
		respuesta.Mensaje = "No estás inscrito en ninguna partida"
	}

	log.Printf("Respuesta: %s", respuesta.Mensaje)
	return respuesta, nil
}

// Implementación del método SincronizarReloj
func (s *server) SincronizarReloj(ctx context.Context, req *pb.SincronizacionRequest) (*pb.SincronizacionResponse, error) {
	clienteID := req.GetIdCliente()
	log.Printf("Recibida solicitud de sincronización de %s", clienteID)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	log.Printf("Reloj vectorial sincronizado con %s: %v", clienteID, respClock)

	return &pb.SincronizacionResponse{
		RelojVectorial: respClock,
		Exito:          true,
	}, nil
}

// Implementación del método UpdateServerStatus
func (s *server) UpdateServerStatus(ctx context.Context, req *pb.ServerStatusUpdateRequest) (*pb.ServerStatusUpdateResponse, error) {
	serverID := req.GetServerId()
	status := req.GetStatus()
	address := req.GetAddress()

	log.Printf("Recibida actualización de estado del servidor de partidas %s: %s en %s",
		serverID, status, address)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Actualizar registro del servidor de partidas
	s.servidoresMutex.Lock()
	if _, existe := s.servidoresPartidas[serverID]; !existe {
		log.Printf("Nuevo servidor de partidas registrado: %s", serverID)
	}

	s.servidoresPartidas[serverID] = &ServidorPartida{
		ID:         serverID,
		Address:    address,
		Status:     status,
		LastUpdate: time.Now(),
	}
	s.servidoresMutex.Unlock()

	log.Printf("Servidor %s registrado/actualizado con dirección '%s' y estado '%s'",
		serverID, address, status)

	return &pb.ServerStatusUpdateResponse{
		StatusCode:     0,
		Message:        "Estado actualizado correctamente",
		RelojVectorial: respClock,
	}, nil
}

// Estructura para representar un servidor de partidas
type ServidorPartida struct {
	ID         string
	Address    string
	Status     string
	LastUpdate time.Time
}

// Implementación del método NotifyMatchResult
func (s *server) NotifyMatchResult(ctx context.Context, req *pb.MatchResultNotification) (*pb.MatchResultResponse, error) {
	matchID := req.GetMatchId()
	ganadorID := req.GetWinnerId()
	perdedorID := req.GetLoserId()
	serverID := req.GetServerId()

	log.Printf("Recibida notificación de resultado de partida %s desde servidor %s: Ganador=%s, Perdedor=%s",
		matchID, serverID, ganadorID, perdedorID)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Actualizar tanto la partida lógica como la referencia al servidor
	s.partidaMutex.Lock()

	// Limpiar todas las referencias de manera atómica
	delete(s.clientePartida, ganadorID)
	delete(s.clientePartida, perdedorID)

	// Actualizar estado de la partida
	partida, existe := s.partidas[matchID]
	if existe {
		// Guardar copias de los IDs de todos los jugadores para limpieza
		jugadores := append([]string{}, partida.Clientes...)

		// Actualizar estado
		partida.Estado = Finalizada
		partida.Resultado = &ResultadoPartida{
			Ganador:  ganadorID,
			Perdedor: perdedorID,
		}

		// Limpiar referencias adicionales si hubiera
		for _, jugadorID := range jugadores {
			if jugadorID != ganadorID && jugadorID != perdedorID {
				delete(s.clientePartida, jugadorID)
			}
		}
	}

	s.partidaMutex.Unlock()

	// Crear nueva partida disponible con el ID original
	s.partidaMutex.Lock()
	s.partidas[matchID] = &Partida{
		ID:       matchID,
		Clientes: []string{},
		Estado:   Esperando,
	}
	s.partidaMutex.Unlock()

	log.Printf("Resultado de partida %s registrado y creada nueva partida con ID %s", matchID, matchID)

	return &pb.MatchResultResponse{
		StatusCode:     0,
		Message:        "Resultado registrado correctamente",
		RelojVectorial: respClock,
	}, nil
}

// Iniciar monitores periódicos
func (s *server) iniciarMonitoresPeriodicos() {
	// Monitor ya existente de emparejamientos
	go func() {
		for {
			time.Sleep(5 * time.Second)
			s.procesarEmparejamientos()
		}
	}()

	// Nuevo monitor para partidas atascadas
	go func() {
		for {
			time.Sleep(3 * time.Second)
			s.liberarPartidasAtascadas()
		}
	}()
}

// Liberar partidas que estén atascadas en estado En Curso por demasiado tiempo
func (s *server) liberarPartidasAtascadas() {
	tiempoMaximo := 12 * time.Second
	ahora := time.Now()

	// Copia de IDs de partidas para evitar deadlocks
	s.partidaMutex.RLock()
	var partidasActivas []string
	for id, partida := range s.partidas {
		if partida.Estado == EnCurso && !partida.InicioPartida.IsZero() {
			partidasActivas = append(partidasActivas, id)
		}
	}
	s.partidaMutex.RUnlock()

	// Procesar cada partida activa
	for _, id := range partidasActivas {
		s.partidaMutex.Lock()
		partida, existe := s.partidas[id]
		if existe && partida.Estado == EnCurso && !partida.InicioPartida.IsZero() {
			duracion := ahora.Sub(partida.InicioPartida)
			if duracion > tiempoMaximo {
				log.Printf("ALERTA: Partida %s atascada por %v. Liberando recursos.", id, duracion)

				// Guardar clientes para liberar referencias
				clientes := append([]string{}, partida.Clientes...)

				// Establecer como finalizada con resultado aleatorio
				partida.Estado = Finalizada
				if len(clientes) >= 2 {
					ganadorIdx := rand.Intn(2)
					perdedorIdx := (ganadorIdx + 1) % 2
					partida.Resultado = &ResultadoPartida{
						Ganador:  clientes[ganadorIdx],
						Perdedor: clientes[perdedorIdx],
					}
				}

				// Liberar referencias de clientes
				for _, clienteID := range clientes {
					delete(s.clientePartida, clienteID)
				}

				// Crear nueva partida disponible con el ID original
				s.partidas[id] = &Partida{
					ID:       id,
					Clientes: []string{},
					Estado:   Esperando,
				}

				log.Printf("Partida %s liberada. Los clientes %v ahora están disponibles.",
					id, clientes)
			}
		}
		s.partidaMutex.Unlock()
	}
}

// Implementación del método AdminGetSystemStatus
func (s *server) AdminGetSystemStatus(ctx context.Context, req *pb.AdminRequest) (*pb.SystemStatusResponse, error) {
	adminID := req.GetAdminId()
	log.Printf("Recibida solicitud de estado del sistema desde administrador %s", adminID)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Preparar respuesta base
	respuesta := &pb.SystemStatusResponse{
		RelojVectorial: respClock,
		StatusCode:     0,
		Message:        "Estado del sistema obtenido con éxito",
	}

	// Obtener información de servidores
	s.servidoresMutex.RLock()
	servers := make([]*pb.ServerStatus, 0, len(s.servidoresPartidas))
	for id, server := range s.servidoresPartidas {
		// Buscar partida activa en este servidor
		activeMatchID := ""
		s.partidaMutex.RLock()
		for _, partida := range s.partidas {
			if partida.ServidorID == id && partida.Estado == EnCurso {
				activeMatchID = partida.ID
				break
			}
		}
		s.partidaMutex.RUnlock()

		servers = append(servers, &pb.ServerStatus{
			ServerId:      id,
			Status:        server.Status,
			Address:       server.Address,
			LastUpdate:    server.LastUpdate.Unix(),
			ActiveMatchId: activeMatchID,
		})
	}
	s.servidoresMutex.RUnlock()
	respuesta.Servers = servers

	// Obtener información de jugadores en cola
	s.colaMutex.RLock()
	queuePlayers := make([]*pb.QueueInfo, 0, len(s.colaJugadores))
	for i, playerID := range s.colaJugadores {
		// En una implementación real, almacenaríamos el tiempo de unión de cada jugador
		// Aquí usamos un tiempo aproximado para demostración
		joinTime := time.Now().Add(-time.Duration(i+1) * time.Minute).Unix()

		queuePlayers = append(queuePlayers, &pb.QueueInfo{
			PlayerId: playerID,
			JoinTime: joinTime,
			Position: int32(i + 1),
		})
	}
	s.colaMutex.RUnlock()
	respuesta.QueuePlayers = queuePlayers

	// Obtener partidas activas
	s.partidaMutex.RLock()
	activeGames := make([]*pb.Partida, 0)
	for _, partida := range s.partidas {
		// Solo incluir partidas con jugadores o en estados relevantes
		if len(partida.Clientes) > 0 || partida.Estado == EnCurso || partida.Estado == Finalizada {
			activeGames = append(activeGames, partida.ToProto())
		}
	}
	s.partidaMutex.RUnlock()
	respuesta.ActiveGames = activeGames

	log.Printf("Enviando información del sistema al administrador %s: %d servidores, %d jugadores en cola, %d partidas activas",
		adminID, len(servers), len(queuePlayers), len(activeGames))

	return respuesta, nil
}

// Implementación del método AdminUpdateServerState
func (s *server) AdminUpdateServerState(ctx context.Context, req *pb.AdminServerUpdateRequest) (*pb.AdminUpdateResponse, error) {
	adminID := req.GetAdminId()
	serverID := req.GetServerId()
	newStatus := req.GetNewStatus()

	log.Printf("Recibida solicitud de cambio de estado para servidor %s a %s desde administrador %s",
		serverID, newStatus, adminID)

	// Incrementar el reloj vectorial del servidor
	s.mutex.Lock()
	s.vectorClock.Increment("Matchmaker")
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()
	s.mutex.Unlock()

	// Preparar respuesta base
	respuesta := &pb.AdminUpdateResponse{
		RelojVectorial: respClock,
		StatusCode:     0,
		Message:        fmt.Sprintf("Estado del servidor %s actualizado a %s", serverID, newStatus),
	}

	// Verificar si el servidor existe
	s.servidoresMutex.Lock()
	servidor, existe := s.servidoresPartidas[serverID]
	if !existe {
		s.servidoresMutex.Unlock()
		respuesta.StatusCode = 1
		respuesta.Message = fmt.Sprintf("Error: Servidor %s no encontrado", serverID)
		return respuesta, nil
	}

	// Casos especiales de manejo
	if newStatus == "RESET" {
		// En caso de reset, verificar si hay partidas activas
		s.partidaMutex.RLock()
		hayPartidaActiva := false
		for _, partida := range s.partidas {
			if partida.ServidorID == serverID && partida.Estado == EnCurso {
				hayPartidaActiva = true
				break
			}
		}
		s.partidaMutex.RUnlock()

		if hayPartidaActiva {
			// Finalizar cualquier partida en curso
			s.partidaMutex.Lock()
			for _, partida := range s.partidas {
				if partida.ServidorID == serverID && partida.Estado == EnCurso {
					// Simular resultado aleatorio
					partida.Estado = Finalizada
					if len(partida.Clientes) >= 2 {
						ganadorIdx := rand.Intn(2)
						perdedorIdx := (ganadorIdx + 1) % 2
						partida.Resultado = &ResultadoPartida{
							Ganador:  partida.Clientes[ganadorIdx],
							Perdedor: partida.Clientes[perdedorIdx],
						}

						// Limpiar referencias
						delete(s.clientePartida, partida.Clientes[0])
						delete(s.clientePartida, partida.Clientes[1])

						log.Printf("Partida %s forzada a finalizar por reset de servidor %s", partida.ID, serverID)
					}
				}
			}
			s.partidaMutex.Unlock()
		}

		// Establecer estado a DISPONIBLE después del reset
		servidor.Status = "DISPONIBLE"
		respuesta.Message = fmt.Sprintf("Servidor %s reseteado y estado establecido a DISPONIBLE", serverID)
	} else {
		// Actualizar el estado normalmente
		servidor.Status = newStatus
	}

	servidor.LastUpdate = time.Now()
	s.servidoresMutex.Unlock()

	log.Printf("Estado del servidor %s actualizado a %s por administrador %s", serverID, newStatus, adminID)

	return respuesta, nil
}

func main() {
	// Inicializar el generador de números aleatorios
	rand.Seed(time.Now().UnixNano())

	// Crear listener TCP en el puerto especificado
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Error al escuchar: %v", err)
	}

	// Crear servidor gRPC
	s := &server{
		vectorClock:        NewVectorClock("Matchmaker"),
		partidas:           make(map[string]*Partida),
		clientePartida:     make(map[string]string),
		colaJugadores:      make([]string, 0),
		servidoresPartidas: make(map[string]*ServidorPartida),
	}

	// Crear algunas partidas iniciales
	s.crearPartidasIniciales(3)

	// Iniciar el procesamiento periódico de emparejamientos
	s.iniciarMonitoresPeriodicos()

	grpcServer := grpc.NewServer()

	// Registrar el servicio de Matchmaker en el servidor
	pb.RegisterMatchmakerServer(grpcServer, s)

	// También registrar el servicio que usan los servidores de partidas
	pb.RegisterMatchmakerServiceServer(grpcServer, s)

	// Registrar el servicio de administración
	pb.RegisterAdminServiceServer(grpcServer, s)

	fmt.Printf("Servidor matchmaker iniciado en %s\n", port)

	// Iniciar servidor
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Error al servir: %v", err)
	}
}
