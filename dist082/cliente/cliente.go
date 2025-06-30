package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	pb "cliente/proto/grpc-server/proto"

	"google.golang.org/grpc"
)

const (
	// Dirección del servidor matchmaker
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

	for id, value := range other {
		if currentValue, exists := vc.clock[id]; !exists || value > currentValue {
			vc.clock[id] = value
		}
	}
}

// Conectar al servidor gRPC con reintentos
func conectarGRPC(address string, maxRetries int, retryDelay time.Duration) (*grpc.ClientConn, error) {
	var conn *grpc.ClientConn
	var err error

	for i := 0; i < maxRetries; i++ {
		conn, err = grpc.Dial(address, grpc.WithInsecure(), grpc.WithBlock(), grpc.WithTimeout(3*time.Second))
		if err == nil {
			return conn, nil
		}

		log.Printf("Intento %d: No se pudo conectar a %s: %v", i+1, address, err)
		time.Sleep(retryDelay)
	}

	return nil, fmt.Errorf("no se pudo conectar a %s después de %d intentos", address, maxRetries)
}

// Mostrar menú de opciones y obtener input del usuario
func mostrarMenu(clienteID string) int {
	fmt.Printf("\n===== %s - MENÚ =====\n", clienteID)
	fmt.Println("1. Inscribirse para emparejamiento")
	fmt.Println("2. Solicitar estado actual")
	fmt.Println("3. Cancelar emparejamiento")
	fmt.Println("0. Salir")
	fmt.Print("\nSeleccione una opción: ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	var opcion int
	fmt.Sscanf(input, "%d", &opcion)
	return opcion
}

// Mostrar información de partidas disponibles
func mostrarPartidas(partidas []*pb.Partida) {
	if len(partidas) == 0 {
		fmt.Println("No hay partidas disponibles")
		return
	}

	fmt.Println("\n=== LISTADO DE PARTIDAS ===")

	servidoresVacios := make(map[string]*pb.Partida)
	partidasActivas := make(map[string]*pb.Partida)

	for _, p := range partidas {
		if len(p.Clientes) == 0 {
			servidoresVacios[p.Id] = p
		}
	}

	for _, p := range partidas {
		if len(p.Clientes) > 0 {
			if p.ServidorId != "" {
				partidasActivas[p.ServidorId] = p
			} else {
				partidasActivas[p.Id] = p
			}
		}
	}

	servidores := []string{"Partida-1", "Partida-2", "Partida-3"}
	for i, servidorID := range servidores {
		if p, existe := partidasActivas[servidorID]; existe {
			estado := p.Estado
			if estado == "" {
				estado = "Esperando"
			}

			status := "Disponible"
			if p.Llena {
				status = "Llena"
			}

			partidaInfo := fmt.Sprintf("%d. %s (Estado: %s, %s) - Jugadores: %s",
				i+1, servidorID, estado, status, strings.Join(p.Clientes, ", "))

			if estado == "Finalizada" && p.Ganador != "" {
				partidaInfo += fmt.Sprintf(" | Ganador: %s, Perdedor: %s", p.Ganador, p.Perdedor)
			}

			fmt.Println(partidaInfo)
		} else if p, existe := servidoresVacios[servidorID]; existe {
			estado := p.Estado
			if estado == "" {
				estado = "Esperando"
			}

			fmt.Printf("%d. %s (Estado: %s, Disponible) - Jugadores:\n",
				i+1, servidorID, estado)
		} else {
			fmt.Printf("%d. %s (Estado: No disponible) - Jugadores:\n", i+1, servidorID)
		}
	}
	fmt.Println("========================")
}

// Inscribir jugador en la cola de emparejamiento
func queuePlayer(client pb.MatchmakerClient, clienteID string, vectorClock *VectorClock, detenerConsulta *bool) {
	// Incrementar el reloj vectorial antes de enviar un mensaje
	vectorClock.Increment(clienteID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud
	req := &pb.PlayerInfoRequest{
		PlayerId:           clienteID,
		GameModePreference: "standard", // Modo por defecto
		RelojVectorial:     vectorClock.Get(),
	}

	// Enviar solicitud
	resp, err := client.QueuePlayer(ctx, req)

	if err != nil {
		log.Printf("[%s] Error en la comunicación: %v", clienteID, err)
		return
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	vectorClock.Update(resp.RelojVectorial)

	// Mostrar respuesta
	if resp.StatusCode == 0 {
		fmt.Printf("[%s] ✅ %s\n", clienteID, resp.Message)
	} else {
		fmt.Printf("[%s] ❌ %s\n", clienteID, resp.Message)
	}

	// Si nos asignaron una partida, mostrar el ID
	if resp.PartidaId != "" {
		fmt.Printf("[%s] Partida asignada: %s\n", clienteID, resp.PartidaId)
	}

	fmt.Printf("[%s] Reloj vectorial actual: %v\n", clienteID, vectorClock.Get())
}

// Consultar estado actual del jugador
func getPlayerStatus(client pb.MatchmakerClient, clienteID string, vectorClock *VectorClock, detenerConsulta *bool, mostrarListado bool, estadoAnterior *string, enPartidaAnterior *bool) string {
	// Guardar valores anteriores
	estabaEnPartida := *enPartidaAnterior

	// Incrementar el reloj vectorial antes de enviar un mensaje
	vectorClock.Increment(clienteID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud
	req := &pb.PlayerStatusRequest{
		PlayerId:       clienteID,
		RelojVectorial: vectorClock.Get(),
	}

	// Enviar solicitud
	resp, err := client.GetPlayerStatus(ctx, req)

	if err != nil {
		log.Printf("[%s] Error en la comunicación: %v", clienteID, err)
		return ""
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	vectorClock.Update(resp.RelojVectorial)

	// Detectar transición de estado
	enPartida := resp.PartidaId != ""
	// Detectar finalización implícita (transición de IN_MATCH a IDLE)
	if estabaEnPartida && !enPartida && resp.PlayerStatus == "IDLE" {
		// Buscar al cliente en alguna partida finalizada
		partidaFinalizada := false
		for _, p := range resp.Partidas {
			if p.Estado == "Finalizada" {
				for _, c := range p.Clientes {
					if c == clienteID {
						partidaFinalizada = true
						break
					}
				}
			}
		}

		if partidaFinalizada {
			resp.PlayerStatus = "MATCH_COMPLETED" // Forzar el estado para detener consultas
			fmt.Printf("[%s] Detectada finalización de partida (transición de IN_MATCH a IDLE)\n", clienteID)
		}
	}

	// Actualizar valores para la próxima llamada
	*estadoAnterior = resp.PlayerStatus
	*enPartidaAnterior = resp.PartidaId != ""

	// Mostrar respuesta
	fmt.Printf("[%s] Estado actual: %s\n", clienteID, resp.PlayerStatus)
	fmt.Printf("[%s] %s\n", clienteID, resp.Mensaje)

	// Si recibimos información de partidas y se debe mostrar el listado, mostrarlo
	if len(resp.Partidas) > 0 && mostrarListado {
		mostrarPartidas(resp.Partidas)
	}

	// Si estamos en una partida, mostrar el ID
	if resp.PartidaId != "" {
		// Nueva lógica para mostrar el servidor si es necesario
		partidaAsignada := resp.GetPartidaId()
		partidaServidor := ""

		// Buscar en la lista de partidas para encontrar el servidor real
		for _, p := range resp.GetPartidas() {
			if p.Id == partidaAsignada {
				// Si la partida tiene un servidor asignado, usar ese ID
				if p.ServidorId != "" {
					partidaServidor = p.ServidorId
				}
				break
			}
		}

		// Mostrar información de la partida asignada
		if resp.PlayerStatus == "IN_MATCH" || resp.PlayerStatus == "IN_QUEUE" {
			// Si hay un servidor específico, mostrarlo
			if partidaServidor != "" && partidaServidor != partidaAsignada {
				fmt.Printf("[%s] Asignado a partida lógica: %s (ejecutándose en servidor: %s)\n",
					clienteID, partidaAsignada, partidaServidor)
			} else {
				fmt.Printf("[%s] Partida asignada: %s\n", clienteID, partidaAsignada)
			}
		}
	}

	// Detener las consultas periódicas si:
	// 1. La partida ha finalizado (MATCH_COMPLETED)
	// 2. O el estado es IDLE y el cliente aparece en alguna partida finalizada
	if detenerConsulta != nil {
		if resp.PlayerStatus == "MATCH_COMPLETED" {
			fmt.Printf("[%s] Deteniendo consultas automáticas debido a finalización.\n", clienteID)
			*detenerConsulta = true
			return ""
		}

		// Verificar si el cliente está en alguna partida finalizada a pesar de estar IDLE
		if resp.PlayerStatus == "IDLE" {
			// Buscar al cliente en partidas finalizadas
			for _, p := range resp.Partidas {
				if p.Estado == "Finalizada" {
					for _, c := range p.Clientes {
						if c == clienteID {
							fmt.Printf("[%s] Deteniendo consultas automáticas. Cliente encontrado en partida finalizada.\n", clienteID)
							*detenerConsulta = true
							return ""
						}
					}
				}
			}

			// NUEVA CONDICIÓN: Si el cliente estaba en una partida y ahora está en IDLE, considerar finalizada
			if estabaEnPartida && !enPartida {
				fmt.Printf("[%s] PARTIDA FINALIZADA. (IN_MATCH a IDLE).\n", clienteID)
				*detenerConsulta = true
				return ""
			}
		}
	}

	fmt.Printf("[%s] Reloj vectorial actual: %v\n", clienteID, vectorClock.Get())

	// Devolver el estado actual para la función de monitoreo
	return resp.PlayerStatus
}

// Consulta periódica del estado del jugador
func consultarEstadoPeriodicamente(client pb.MatchmakerClient, clienteID string, vectorClock *VectorClock, segundos int, detener *bool) {
	var estadoAnterior string
	var enPartidaAnterior bool
	var tiempoEnPartida time.Time

	for i := 0; i < segundos && !*detener; i++ {
		time.Sleep(5 * time.Second)

		if *detener {
			fmt.Printf("[%s] Deteniendo consultas periódicas según lo solicitado.\n", clienteID)
			return
		}

		fmt.Printf("\n[%s] Consultando estado automáticamente...\n", clienteID)

		// Llamar a getPlayerStatus y obtener el estado actual
		estadoActual := getPlayerStatus(client, clienteID, vectorClock, detener, false, &estadoAnterior, &enPartidaAnterior)

		// Verificar si estamos atascados en IN_MATCH
		if estadoActual == "IN_MATCH" {
			if tiempoEnPartida.IsZero() {
				// Primera vez que detectamos IN_MATCH, iniciar contador
				tiempoEnPartida = time.Now()
			} else if time.Since(tiempoEnPartida) > 12*time.Second {
				// Hemos estado más de 12 segundos en IN_MATCH sin cambios
				fmt.Printf("[%s] ⚠️ Partida posiblemente atascada por %v. Intentando cancelar...\n",
					clienteID, time.Since(tiempoEnPartida))

				// Intentar cancelar automáticamente
				cancelQueuePlayer(client, clienteID, vectorClock, detener)

				// Detener esta consulta periódica
				*detener = true
				return
			}
		} else {
			// Resetear el contador si no estamos en IN_MATCH
			tiempoEnPartida = time.Time{}
		}
	}
}

// Cancelar emparejamiento actual
func cancelQueuePlayer(client pb.MatchmakerClient, clienteID string, vectorClock *VectorClock, detenerConsulta *bool) {
	// Esta función podría ser implementada como un nuevo método en la API,
	// pero para compatibilidad, usaremos GetPlayerStatus con un manejo especial

	// Incrementar el reloj vectorial antes de enviar un mensaje
	vectorClock.Increment(clienteID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud como un mensaje especial (podría ser mejorado)
	req := &pb.PlayerInfoRequest{
		PlayerId:           clienteID,
		GameModePreference: "CANCEL", // Indicador especial
		RelojVectorial:     vectorClock.Get(),
	}

	// Enviar solicitud
	resp, err := client.QueuePlayer(ctx, req)

	if err != nil {
		log.Printf("[%s] Error en la comunicación: %v", clienteID, err)
		return
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	vectorClock.Update(resp.RelojVectorial)

	// Mostrar respuesta
	if resp.StatusCode == 0 {
		fmt.Printf("[%s] ✅ Emparejamiento cancelado exitosamente\n", clienteID)
	} else {
		fmt.Printf("[%s] ❌ %s\n", clienteID, resp.Message)
	}

	// Detener consulta periódica
	if detenerConsulta != nil {
		*detenerConsulta = true
	}

	fmt.Printf("[%s] Reloj vectorial actual: %v\n", clienteID, vectorClock.Get())
}

// Función principal
func main() {
	// Obtener ID del cliente desde variable de entorno
	clienteID := os.Getenv("CLIENTE_ID")
	if clienteID == "" {
		clienteID = "ClienteDesconocido"
	}
	log.Printf("Iniciando %s", clienteID)

	// Inicializar el reloj vectorial para este cliente
	vectorClock := NewVectorClock(clienteID)

	// Configurar conexión gRPC con reintentos
	conn, err := conectarGRPC(matchmakerAddr, 5, 2*time.Second)
	if err != nil {
		log.Fatalf("[%s] Error al conectar: %v", clienteID, err)
	}
	defer conn.Close()

	log.Printf("[%s] Conexión establecida con %s", clienteID, matchmakerAddr)

	// Crear cliente gRPC
	client := pb.NewMatchmakerClient(conn)

	// Variables para control de consulta periódica
	consultando := false
	detenerConsulta := false
	var estadoAnterior string
	var enPartidaAnterior bool

	// Bucle principal del menú
	for {
		opcion := mostrarMenu(clienteID)

		switch opcion {
		case 1:
			fmt.Printf("[%s] Enviando solicitud de inscripción...\n", clienteID)
			queuePlayer(client, clienteID, vectorClock, &detenerConsulta)

			// Iniciar consulta periódica si no está activa
			if !consultando {
				consultando = true
				detenerConsulta = false
				go consultarEstadoPeriodicamente(client, clienteID, vectorClock, 12, &detenerConsulta)
			}

		case 2:
			fmt.Printf("[%s] Solicitando estado actual...\n", clienteID)
			getPlayerStatus(client, clienteID, vectorClock, &detenerConsulta, true, &estadoAnterior, &enPartidaAnterior)

			// Si estamos consultando periódicamente y recibimos MATCH_COMPLETED,
			// actualizar también la variable de control
			if consultando && detenerConsulta {
				consultando = false
			}

		case 3:
			fmt.Printf("[%s] Cancelando emparejamiento...\n", clienteID)
			cancelQueuePlayer(client, clienteID, vectorClock, &detenerConsulta)

			// Detener consulta periódica si está activa
			if consultando {
				detenerConsulta = true
				consultando = false
			}

		case 0:
			fmt.Printf("[%s] Saliendo...\n", clienteID)
			// Detener consulta periódica si está activa
			if consultando {
				detenerConsulta = true
			}
			return

		default:
			fmt.Println("Opción no válida. Por favor, intente de nuevo.")
		}

		time.Sleep(1 * time.Second)
	}
}
