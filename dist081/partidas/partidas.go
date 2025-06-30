package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	pb "partidas/proto/grpc-server/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Estado de una partida
type EstadoPartida string

const (
	Disponible EstadoPartida = "DISPONIBLE"
	Ocupado    EstadoPartida = "OCUPADO"
	EnCurso    EstadoPartida = "EN_CURSO"
	Finalizado EstadoPartida = "FINALIZADO"
	Caido      EstadoPartida = "CAIDO"
)

// Dirección del servidor matchmaker
const (
	matchmakerAddr = "10.35.168.94:50051"
)

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

// Resultado de una partida
type ResultadoPartida struct {
	Ganador  string
	Perdedor string
	Mensaje  string
}

// Estructura para representar el estado interno de una partida
type PartidaState struct {
	ID        string
	Estado    EstadoPartida
	Jugadores []string
	Resultado *ResultadoPartida
	mutex     sync.Mutex
}

// Servidor implementando la interfaz PartidaService
type partidaServer struct {
	pb.UnimplementedPartidaServiceServer
	partidaID    string // ID de esta partida
	estado       EstadoPartida
	address      string
	vectorClock  *VectorClock
	estadoMutex  sync.RWMutex
	partidaState *PartidaState
}

// Función para conectar con reintentos
func conectarGRPC(address string, maxRetries int, retryDelay time.Duration) (*grpc.ClientConn, error) {
	var conn *grpc.ClientConn
	var err error

	for i := 0; i < maxRetries; i++ {
		conn, err = grpc.Dial(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(), grpc.WithTimeout(3*time.Second))
		if err == nil {
			return conn, nil // Conexión exitosa
		}

		log.Printf("Intento %d: No se pudo conectar a %s: %v", i+1, address, err)
		time.Sleep(retryDelay) // Esperar antes de reintentar
	}

	return nil, fmt.Errorf("no se pudo conectar a %s después de %d intentos", address, maxRetries)
}

// Actualizar el estado del servidor de partidas en el matchmaker
func (s *partidaServer) updateServerStatus(status EstadoPartida) error {
	// Configurar conexión gRPC con reintentos
	conn, err := conectarGRPC(matchmakerAddr, 5, 2*time.Second)
	if err != nil {
		log.Printf("[%s] Error al conectar con matchmaker: %v", s.partidaID, err)
		return err
	}
	defer conn.Close()

	// Crear cliente gRPC para el matchmaker
	client := pb.NewMatchmakerServiceClient(conn)

	// Incrementar el reloj vectorial antes de enviar un mensaje
	s.vectorClock.Increment(s.partidaID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud de actualización de estado
	req := &pb.ServerStatusUpdateRequest{
		ServerId:       s.partidaID,
		Status:         string(status),
		Address:        s.address,
		RelojVectorial: s.vectorClock.Get(),
	}

	// Enviar solicitud
	resp, err := client.UpdateServerStatus(ctx, req)
	if err != nil {
		log.Printf("[%s] Error al actualizar estado: %v", s.partidaID, err)
		return err
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	s.vectorClock.Update(resp.GetRelojVectorial())

	// Actualizar estado interno
	s.estadoMutex.Lock()
	s.estado = status
	s.estadoMutex.Unlock()

	log.Printf("[%s] Estado actualizado a %s (código: %d, mensaje: %s)",
		s.partidaID, status, resp.GetStatusCode(), resp.GetMessage())

	return nil
}

// AssignMatch implementa el método AssignMatch del servidor gRPC
func (s *partidaServer) AssignMatch(ctx context.Context, req *pb.AssignMatchRequest) (*pb.AssignMatchResponse, error) {
	log.Printf("[%s] Recibida solicitud AssignMatch para match %s con jugadores: %v",
		s.partidaID, req.GetMatchId(), req.GetPlayerIds())

	// Incrementar el reloj vectorial
	s.vectorClock.Increment(s.partidaID)
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()

	// Verificar que hay exactamente 2 jugadores
	if len(req.GetPlayerIds()) != 2 {
		log.Printf("[%s] Error: Se requieren exactamente 2 jugadores, recibidos %d",
			s.partidaID, len(req.GetPlayerIds()))
		return &pb.AssignMatchResponse{
			StatusCode:     1, // Error
			Message:        "Se requieren exactamente 2 jugadores para iniciar una partida",
			RelojVectorial: respClock,
		}, nil
	}

	// Preparar respuesta base
	respuesta := &pb.AssignMatchResponse{
		StatusCode:     0, // Éxito por defecto
		RelojVectorial: respClock,
	}

	// Actualizar estado interno de la partida
	s.partidaState.mutex.Lock()
	s.partidaState.ID = req.GetMatchId()
	s.partidaState.Jugadores = req.GetPlayerIds()
	s.partidaState.Estado = EnCurso
	s.partidaState.mutex.Unlock()

	// Actualizar estado en el matchmaker
	err := s.updateServerStatus(Ocupado)
	if err != nil {
		log.Printf("[%s] Error al actualizar estado a Ocupado: %v", s.partidaID, err)
		respuesta.StatusCode = 2
		respuesta.Message = "Error al actualizar estado del servidor de partidas"
		return respuesta, nil
	}

	// Iniciar simulación de partida en una goroutine
	go s.simularPartida()

	respuesta.Message = fmt.Sprintf("Partida %s iniciada con jugadores: %v",
		req.GetMatchId(), req.GetPlayerIds())

	log.Printf("[%s] %s", s.partidaID, respuesta.Message)

	return respuesta, nil
}

// ObtenerEstadoPartida implementa el método para consultar el estado de la partida
func (s *partidaServer) ObtenerEstadoPartida(ctx context.Context, req *pb.EstadoPartidaRequest) (*pb.EstadoPartidaResponse, error) {
	log.Printf("[%s] Recibida solicitud de estado para partida: %s",
		s.partidaID, req.GetPartidaId())

	// Incrementar el reloj vectorial
	s.vectorClock.Increment(s.partidaID)
	s.vectorClock.Update(req.GetRelojVectorial())
	respClock := s.vectorClock.Get()

	// Verificar si la partida existe
	s.partidaState.mutex.Lock()
	defer s.partidaState.mutex.Unlock()

	// Preparar respuesta
	respuesta := &pb.EstadoPartidaResponse{
		RelojVectorial: respClock,
	}

	// Si la partida no coincide
	if s.partidaState.ID != req.GetPartidaId() && s.partidaState.ID != "" {
		respuesta.Encontrada = false
		respuesta.Mensaje = "Partida no encontrada en este servidor"
		return respuesta, nil
	}

	// Si no hay partida asignada
	if s.partidaState.ID == "" {
		respuesta.Encontrada = false
		respuesta.Mensaje = "Este servidor no tiene ninguna partida asignada"
		return respuesta, nil
	}

	// Partida encontrada
	respuesta.Encontrada = true
	respuesta.PartidaId = s.partidaState.ID
	respuesta.Estado = string(s.partidaState.Estado)

	// Si hay jugadores, asignarlos
	if len(s.partidaState.Jugadores) >= 2 {
		respuesta.Jugador1 = s.partidaState.Jugadores[0]
		respuesta.Jugador2 = s.partidaState.Jugadores[1]
	}

	// Si hay un resultado, incluirlo
	if s.partidaState.Resultado != nil {
		respuesta.Ganador = s.partidaState.Resultado.Ganador
		respuesta.Mensaje = s.partidaState.Resultado.Mensaje
	} else {
		respuesta.Mensaje = fmt.Sprintf("Partida %s en estado %s",
			s.partidaState.ID, s.partidaState.Estado)
	}

	log.Printf("[%s] Enviando estado: %+v", s.partidaID, respuesta)

	return respuesta, nil
}

// Simulación de una partida en curso
func (s *partidaServer) simularPartida() {
	log.Printf("[%s] Iniciando simulación de partida...", s.partidaID)
	log.Print("FUNCIONA")

	// Crear un canal para timeout de seguridad
	timeoutChan := time.After(15 * time.Second)

	// Esperar un tiempo aleatorio entre 5 y 10 segundos
	rand.Seed(time.Now().UnixNano())
	duracion := 5 + rand.Intn(6)
	tiempoSimulacion := time.After(time.Duration(duracion) * time.Second)

	// Esperar a que termine la simulación o el timeout
	select {
	case <-tiempoSimulacion:
		// Procesamiento normal
	case <-timeoutChan:
		log.Printf("[%s] ⚠️ TIMEOUT de seguridad activado en simulación de partida", s.partidaID)
	}

	s.partidaState.mutex.Lock()

	// Verificar que la partida sigue en curso
	if s.partidaState.Estado != EnCurso || len(s.partidaState.Jugadores) != 2 {
		s.partidaState.mutex.Unlock()
		log.Printf("[%s] La simulación de partida se canceló porque el estado cambió", s.partidaID)
		return
	}

	// Elegir ganador aleatoriamente
	ganadorIdx := rand.Intn(2)
	perdedorIdx := (ganadorIdx + 1) % 2

	// Registrar el resultado
	s.partidaState.Resultado = &ResultadoPartida{
		Ganador:  s.partidaState.Jugadores[ganadorIdx],
		Perdedor: s.partidaState.Jugadores[perdedorIdx],
		Mensaje: fmt.Sprintf("El jugador %s ha ganado contra %s",
			s.partidaState.Jugadores[ganadorIdx],
			s.partidaState.Jugadores[perdedorIdx]),
	}

	// Cambiar estado a finalizado
	s.partidaState.Estado = Finalizado

	log.Printf("[%s] Partida finalizada. %s",
		s.partidaID, s.partidaState.Resultado.Mensaje)

	s.partidaState.mutex.Unlock()

	// Notificar resultado al matchmaker
	s.notificarResultadoAMatchmaker()

	// Actualizar estado en el matchmaker
	err := s.updateServerStatus(Disponible)
	if err != nil {
		log.Printf("[%s] Error al actualizar estado a Disponible: %v", s.partidaID, err)
	}

	// Después de un tiempo, reiniciar el estado para estar listo para nueva partida
	time.Sleep(30 * time.Second)

	s.partidaState.mutex.Lock()
	s.partidaState.ID = ""
	s.partidaState.Jugadores = nil
	s.partidaState.Resultado = nil
	s.partidaState.mutex.Unlock()

	log.Printf("[%s] Servidor de partidas listo para una nueva partida", s.partidaID)
}

// En partidas.go, después de finalizar una partida
func (s *partidaServer) notificarResultadoAMatchmaker() {
	// Verificar que existe un resultado
	s.partidaState.mutex.Lock()
	if s.partidaState.Resultado == nil || s.partidaState.Estado != Finalizado {
		s.partidaState.mutex.Unlock()
		log.Printf("[%s] No se puede notificar resultado: partida no finalizada o sin resultado", s.partidaID)
		return
	}

	// Capturar datos del resultado para usar después de liberar el mutex
	matchID := s.partidaState.ID
	ganador := s.partidaState.Resultado.Ganador
	perdedor := s.partidaState.Resultado.Perdedor
	s.partidaState.mutex.Unlock()

	log.Printf("[%s] Notificando resultado al matchmaker: ganador=%s, perdedor=%s",
		s.partidaID, ganador, perdedor)

	// Configurar conexión gRPC con reintentos
	conn, err := conectarGRPC(matchmakerAddr, 5, 2*time.Second)
	if err != nil {
		log.Printf("[%s] Error al conectar con matchmaker para notificar resultado: %v", s.partidaID, err)
		return
	}
	defer conn.Close()

	// Crear cliente gRPC para el matchmaker
	client := pb.NewMatchmakerServiceClient(conn)

	// Incrementar el reloj vectorial antes de enviar un mensaje
	s.vectorClock.Increment(s.partidaID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud con datos del resultado
	req := &pb.MatchResultNotification{
		MatchId:        matchID,
		WinnerId:       ganador,
		LoserId:        perdedor,
		ServerId:       s.partidaID,
		RelojVectorial: s.vectorClock.Get(),
	}

	// Enviar solicitud
	resp, err := client.NotifyMatchResult(ctx, req)
	if err != nil {
		log.Printf("[%s] Error al notificar resultado: %v", s.partidaID, err)
		return
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	s.vectorClock.Update(resp.GetRelojVectorial())

	log.Printf("[%s] Resultado notificado correctamente al matchmaker (código: %d, mensaje: %s)",
		s.partidaID, resp.GetStatusCode(), resp.GetMessage())

	// Restablecer el estado rápidamente en lugar de esperar
	s.partidaState.mutex.Lock()
	s.partidaState.ID = ""
	s.partidaState.Jugadores = nil
	s.partidaState.Resultado = nil
	s.partidaState.Estado = Disponible
	s.partidaState.mutex.Unlock()

	// Actualizar inmediatamente el estado a DISPONIBLE
	err = s.updateServerStatus(Disponible)
	if err != nil {
		log.Printf("[%s] Error al actualizar estado a Disponible: %v", s.partidaID, err)
	}
}

func main() {
	partidaID := os.Getenv("PARTIDA_ID")
	if partidaID == "" {
		partidaID = "Partida-Unknown"
	}

	partidaPort := os.Getenv("PARTIDA_PORT")
	if partidaPort == "" {
		partidaPort = "50052" // Puerto por defecto
	}

	// Dirección completa del servidor
	address := fmt.Sprintf("0.0.0.0:%s", partidaPort)

	log.Printf("Iniciando servidor de partidas %s en %s", partidaID, address)

	// Crear listener TCP
	lis, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Error al escuchar: %v", err)
	}

	var serverIP string
	switch partidaID {
	case "Partida-1":
		serverIP = "10.35.168.91"
	case "Partida-2":
		serverIP = "10.35.168.92"
	case "Partida-3":
		serverIP = "10.35.168.93"
	default:
		serverIP = "0.0.0.0"
	}

	s := &partidaServer{
		partidaID:   partidaID,
		estado:      Disponible,
		address:     fmt.Sprintf("%s:%s", serverIP, partidaPort),
		vectorClock: NewVectorClock(partidaID),
		partidaState: &PartidaState{
			Estado: Disponible,
		},
	}

	log.Printf("Registrando servidor con dirección: %s", s.address)

	grpcServer := grpc.NewServer()
	pb.RegisterPartidaServiceServer(grpcServer, s)

	go func() {
		time.Sleep(3 * time.Second)
		err := s.updateServerStatus(Disponible)
		if err != nil {
			log.Printf("Error al registrar servidor con matchmaker: %v", err)
		} else {
			log.Printf("Servidor de partidas %s registrado con matchmaker", partidaID)
		}
	}()

	log.Printf("Servidor de partidas %s listo para recibir solicitudes", partidaID)

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Error al servir: %v", err)
	}
}
