package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "clienteadmin/proto/grpc-server/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// Dirección del servidor matchmaker
	matchmakerAddr = "10.35.168.94:50051"
	// ID del cliente administrador
	adminID = "AdminClient"
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

// Función para mostrar el menú principal
func mostrarMenu() int {
	fmt.Println("\n===== PANEL DE ADMINISTRACIÓN =====")
	fmt.Println("1. Ver Estado de Servidores")
	fmt.Println("2. Ver Colas de Jugadores")
	fmt.Println("3. Ver Partidas Activas")
	fmt.Println("4. Cambiar Estado de Servidor")
	fmt.Println("0. Salir")
	fmt.Print("\nSeleccione una opción: ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	opcion, _ := strconv.Atoi(input)
	return opcion
}

// Función para obtener el estado completo del sistema
func getSystemStatus(client pb.AdminServiceClient, vectorClock *VectorClock) (*pb.SystemStatusResponse, error) {
	// Incrementar el reloj vectorial antes de enviar un mensaje
	vectorClock.Increment(adminID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud
	req := &pb.AdminRequest{
		AdminId:        adminID,
		RelojVectorial: vectorClock.Get(),
	}

	// Enviar solicitud
	resp, err := client.AdminGetSystemStatus(ctx, req)
	if err != nil {
		return nil, err
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	vectorClock.Update(resp.GetRelojVectorial())

	return resp, nil
}

// Función para mostrar el estado de los servidores
func mostrarEstadoServidores(resp *pb.SystemStatusResponse) {
	fmt.Println("\n===== ESTADO DE SERVIDORES =====")

	if len(resp.GetServers()) == 0 {
		fmt.Println("No hay servidores registrados en el sistema.")
		return
	}

	fmt.Println("ID\t\tESTADO\t\tDIRECCIÓN\t\tÚLTIMA ACTUALIZACIÓN\tPARTIDA ACTIVA")
	fmt.Println("--------------------------------------------------------------------------------------------")

	for _, server := range resp.GetServers() {
		lastUpdate := time.Unix(server.GetLastUpdate(), 0).Format("15:04:05")
		matchID := server.GetActiveMatchId()
		if matchID == "" {
			matchID = "-"
		}

		fmt.Printf("%s\t%s\t\t%s\t%s\t\t%s\n",
			server.GetServerId(),
			server.GetStatus(),
			server.GetAddress(),
			lastUpdate,
			matchID)
	}
}

// Función para mostrar las colas de jugadores
func mostrarColasJugadores(resp *pb.SystemStatusResponse) {
	fmt.Println("\n===== COLA DE JUGADORES =====")

	if len(resp.GetQueuePlayers()) == 0 {
		fmt.Println("No hay jugadores en cola de espera.")
		return
	}

	fmt.Println("POSICIÓN\tJUGADOR\t\tTIEMPO EN COLA")
	fmt.Println("---------------------------------------")

	for _, player := range resp.GetQueuePlayers() {
		// Calcular tiempo en cola
		joinTime := time.Unix(player.GetJoinTime(), 0)
		tiempoEnCola := time.Since(joinTime).Round(time.Second)

		fmt.Printf("%d\t\t%s\t\t%s\n",
			player.GetPosition(),
			player.GetPlayerId(),
			tiempoEnCola)
	}
}

// Función para mostrar partidas activas
func mostrarPartidasActivas(resp *pb.SystemStatusResponse) {
	fmt.Println("\n===== PARTIDAS ACTIVAS =====")

	if len(resp.GetActiveGames()) == 0 {
		fmt.Println("No hay partidas activas en este momento.")
		return
	}

	fmt.Println("ID PARTIDA\tESTADO\t\tSERVIDOR\t\tJUGADORES")
	fmt.Println("------------------------------------------------------------------")

	for _, partida := range resp.GetActiveGames() {
		servidorId := partida.GetServidorId()
		if servidorId == "" {
			servidorId = "Local"
		}

		fmt.Printf("%s\t%s\t%s\t\t%s\n",
			partida.GetId(),
			partida.GetEstado(),
			servidorId,
			strings.Join(partida.GetClientes(), ", "))

		// Si la partida está finalizada, mostrar ganador y perdedor
		if partida.GetEstado() == "Finalizada" {
			if partida.GetGanador() != "" {
				fmt.Printf("\tResultado: Ganador: %s, Perdedor: %s\n",
					partida.GetGanador(),
					partida.GetPerdedor())
			}
		}
	}
}

// Función para cambiar el estado de un servidor
func cambiarEstadoServidor(client pb.AdminServiceClient, vectorClock *VectorClock) {
	// Primero obtenemos la lista de servidores para mostrarla
	resp, err := getSystemStatus(client, vectorClock)
	if err != nil {
		fmt.Printf("Error al obtener lista de servidores: %v\n", err)
		return
	}

	if len(resp.GetServers()) == 0 {
		fmt.Println("No hay servidores disponibles para modificar.")
		return
	}

	// Mostrar la lista de servidores
	mostrarEstadoServidores(resp)

	// Solicitar ID del servidor a modificar
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\nIngrese el ID del servidor a modificar: ")
	serverID, _ := reader.ReadString('\n')
	serverID = strings.TrimSpace(serverID)

	// Validar que el servidor existe
	serverExists := false
	for _, server := range resp.GetServers() {
		if server.GetServerId() == serverID {
			serverExists = true
			break
		}
	}

	if !serverExists {
		fmt.Println("Error: El ID de servidor ingresado no existe.")
		return
	}

	// Mostrar opciones de estados
	fmt.Println("\nEstados disponibles:")
	fmt.Println("1. DISPONIBLE")
	fmt.Println("2. OCUPADO")
	fmt.Println("3. CAIDO")
	fmt.Println("4. RESET")

	fmt.Print("Seleccione el nuevo estado: ")
	estadoInput, _ := reader.ReadString('\n')
	estadoInput = strings.TrimSpace(estadoInput)

	estadoInt, err := strconv.Atoi(estadoInput)
	if err != nil || estadoInt < 1 || estadoInt > 4 {
		fmt.Println("Error: Opción no válida.")
		return
	}

	// Convertir la opción a estado
	estados := []string{"DISPONIBLE", "OCUPADO", "CAIDO", "RESET"}
	nuevoEstado := estados[estadoInt-1]

	// Incrementar el reloj vectorial
	vectorClock.Increment(adminID)

	// Contexto con timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// Crear solicitud
	req := &pb.AdminServerUpdateRequest{
		AdminId:        adminID,
		ServerId:       serverID,
		NewStatus:      nuevoEstado,
		RelojVectorial: vectorClock.Get(),
	}

	// Enviar solicitud
	resp2, err := client.AdminUpdateServerState(ctx, req)
	if err != nil {
		fmt.Printf("Error al cambiar estado del servidor: %v\n", err)
		return
	}

	// Actualizar nuestro reloj vectorial con la respuesta
	vectorClock.Update(resp2.GetRelojVectorial())

	// Mostrar respuesta
	if resp2.GetStatusCode() == 0 {
		fmt.Printf("✅ El estado del servidor %s ha sido cambiado a %s\n", serverID, nuevoEstado)
	} else {
		fmt.Printf("❌ Error al cambiar estado: %s\n", resp2.GetMessage())
	}
}

// Función principal
func main() {
	log.Printf("Iniciando Cliente Administrador %s", adminID)

	// Inicializar el reloj vectorial
	vectorClock := NewVectorClock(adminID)

	// Configurar conexión gRPC con reintentos
	conn, err := conectarGRPC(matchmakerAddr, 5, 2*time.Second)
	if err != nil {
		log.Fatalf("[%s] Error al conectar: %v", adminID, err)
	}
	defer conn.Close()

	log.Printf("[%s] Conexión establecida con %s", adminID, matchmakerAddr)

	// Crear cliente gRPC
	client := pb.NewAdminServiceClient(conn)

	// Bucle principal del menú
	for {
		opcion := mostrarMenu()

		switch opcion {
		case 1: // Ver Estado de Servidores
			fmt.Printf("[%s] Obteniendo estado de servidores...\n", adminID)
			resp, err := getSystemStatus(client, vectorClock)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}
			mostrarEstadoServidores(resp)

		case 2: // Ver Colas de Jugadores
			fmt.Printf("[%s] Obteniendo información de colas de jugadores...\n", adminID)
			resp, err := getSystemStatus(client, vectorClock)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}
			mostrarColasJugadores(resp)

		case 3: // Ver Partidas Activas
			fmt.Printf("[%s] Obteniendo información de partidas activas...\n", adminID)
			resp, err := getSystemStatus(client, vectorClock)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}
			mostrarPartidasActivas(resp)

		case 4: // Cambiar Estado de Servidor
			fmt.Printf("[%s] Cambiando estado de servidor...\n", adminID)
			cambiarEstadoServidor(client, vectorClock)

		case 0: // Salir
			fmt.Printf("[%s] Saliendo...\n", adminID)
			return

		default:
			fmt.Println("Opción no válida. Por favor, intente de nuevo.")
		}

		time.Sleep(1 * time.Second)
	}
}
