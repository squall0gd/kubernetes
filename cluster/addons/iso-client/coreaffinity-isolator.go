package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/golang/glog"
	// For newer version of k8s use this package
	//	"k8s.io/apimachinery/pkg/util/uuid"

	aff "k8s.io/kubernetes/cluster/addons/iso-client/coreaffinity"
	"k8s.io/kubernetes/cluster/addons/iso-client/discovery"
	opaq "k8s.io/kubernetes/cluster/addons/iso-client/opaque"
	"k8s.io/kubernetes/pkg/kubelet/api/v1alpha1/lifecycle"
)

const (
	// kubelet eventDispatcher address
	eventDispatcherAddress = "localhost:5433"
	// iso-client own address
	eventHandlerLocalAddress = "localhost:5444"
	// name of isolator
	name = "coreaffinity"
)

// TODO: handle more than just unregistering  evenDispatcherClient
func handleSIGTERM(sigterm chan os.Signal, client *aff.EventDispatcherClient, opaque *opaq.OpaqueIntegerResourceAdvertiser) {
	<-sigterm
	unregisterRequest := &lifecycle.UnregisterRequest{
		Name:  client.Name,
		Token: client.Token,
	}

	if _, err := client.Unregister(client.Ctx, unregisterRequest); err != nil {
		opaque.RemoveOpaqueResource()
		glog.Fatalf("Failed to unregister handler: %v")
	}
	glog.Infof("Unregistering custom-isolator: %s", name)

	if err := opaque.RemoveOpaqueResource(); err != nil {
		glog.Fatalf("Failed to remove opaque resources: %v", err)
	}

	os.Exit(0)

}

// TODO: split it to smaller functions
func main() {
	flag.Parse()
	glog.Info("Starting ...")
	topology, err := discovery.DiscoverTopology()
	if err != nil {
		glog.Fatalf(err.Error())
	}

	opaque, err := opaq.NewOpaqueIntegerResourceAdvertiser(name, fmt.Sprintf("%d", topology.GetCpusNum()))
	if err != nil {
		glog.Fatalf("Cannot create opaque resource advertiser: %v", err)
	}
	if err = opaque.AdvertiseOpaqueResource(); err != nil {
		glog.Fatalf("Failed to advertise opaque resources: %v", err)
	}

	var wg sync.WaitGroup
	// Starting eventHandlerServer
	server := aff.NewEventHandler(name, eventHandlerLocalAddress, topology)
	err = server.RegisterEventHandler()
	if err != nil {
		glog.Fatalf("Cannot register EventHandler: %v", err)
		os.Exit(1)
	}
	wg.Add(1)
	go server.Serve(wg)

	// Sending address of local eventHandlerServer
	client, err := aff.NewEventDispatcherClient(name, eventDispatcherAddress, eventHandlerLocalAddress)

	if err != nil {
		glog.Fatalf("Cannot create eventDispatcherClient: %v", err)
		os.Exit(1)
	}

	reply, err := client.Register()
	if err != nil {
		glog.Fatalf("Failed to register handler: %v . Reply: %v", err, reply)
		os.Exit(1)
	}
	glog.Infof("Registering eventDispatcherClient. Reply: %v", reply)

	// Handling SigTerm
	sigterm := make(chan os.Signal, 2)
	signal.Notify(sigterm, os.Interrupt, syscall.SIGTERM)
	go handleSIGTERM(sigterm, client, opaque)

	wg.Wait()
}
