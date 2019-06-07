package chain

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"

	"0chain.net/chaincore/config"
	"0chain.net/chaincore/node"
	"0chain.net/core/common"
	"0chain.net/core/datastore"
	"0chain.net/core/ememorystore"
	. "0chain.net/core/logging"
	//"github.com/spf13/viper"
	"go.uber.org/zap"
)

//MBType a type to indicate magic block is of a declared type
type MBType string

const (
	//CURR a constant type to indicate magic block is of CURRent type
	CURR MBType = "CURR"
	//NEXT a constant type to indicate magic block is of NEXT type
	NEXT MBType = "NEXT"
)

// MbLastRoundUninitialized indicates last round needs to be recalced
const MbLastRoundUninitialized = -1

//MagicBlock to create and track active sets
type MagicBlock struct {
	datastore.IDField
	MagicBlockNumber   int64        `json:"magic_block_number,omitempty"`
	StartingRound      int64        `json:"starting_round,omitempty"`
	EstimatedLastRound int64        `json:"estimated_last_round,omitempty"`
	TypeOfMB           MBType       `json:"type_of_mb"`
	AllMiners          *node.Pool   `json:"all_miners,omitempty"`         //this is the pool of all miners that have stakes
	AllSharders        *node.Pool   `json:"all_sharders,omitempty"`       //this is the pool of all sharders that have stakes
	DKGSetMiners       *node.Pool   `json:"dkgset_miners,omitempty"`      //this is the pool of all Miners selected for DKG process
	ActiveSetMiners    *node.Pool   `json:"activeset_miners,omitempty"`   //this is the pool of miners participating in the blockchain
	ActiveSetSharders  *node.Pool   `json:"activeset_sharders,omitempty"` //this is the pool of sharders participaing in the blockchain
	VcVrfShare         *VCVRFShare  `json:"-"`                            //Self VcVRFShare that is shared with others
	SecretKeyGroupStr  string       `json:"secret_key_group_str"`
	RandomSeed         int64        `json:"random_seed,omitempty"`      //RandomSeed generated by aggregating VcVRFShares
	PrevRandomSeed     int64        `json:"prev_random_seed,omitempty"` //PrevRandomSeed that will be used for generating Randomseed
	ActiveSetMaxSize   int          `json:"activeset_max"`
	ActiveSetMinSize   int          `json:"activeset_min"`
	Mutex              sync.RWMutex `json:"-"`
	minerPerm          []int
	recVcVrfSharesMap  map[string]*VCVRFShare
}

var magicBlockMetadata *datastore.EntityMetadataImpl

//GetEntityMetadata entity metadata for DKGSummary
func (mb *MagicBlock) GetEntityMetadata() datastore.EntityMetadata {
	return magicBlockMetadata
}

/*GetKey - returns the MagicBlock number as the key Is this right? should use "curr" and "next"*/
func (mb *MagicBlock) GetKey() datastore.Key {
	return datastore.ToKey(fmt.Sprintf("%v", mb.TypeOfMB))
}

//MagicBlockProvider the provider for MagicBlock
func MagicBlockProvider() datastore.Entity {
	mb := &MagicBlock{}
	return mb
}

//SetupMagicBlockStore magicblock db definition
func SetupMagicBlockStore(store datastore.Store) {
	magicBlockMetadata = datastore.MetadataProvider()
	magicBlockMetadata.Name = "magicblock"
	magicBlockMetadata.DB = "magicblockdb"
	magicBlockMetadata.Store = store
	magicBlockMetadata.Provider = MagicBlockProvider
	magicBlockMetadata.IDColumnName = "magic_block_number" //we should use "curr" and "next"?
	datastore.RegisterEntityMetadata("magicblock", magicBlockMetadata)
}

// SetupMagicBlockDB MagicBlock DB store setup
func SetupMagicBlockDB() {
	db, err := ememorystore.CreateDB("data/rocksdb/magicblock")
	if err != nil {
		panic(err)
	}
	ememorystore.AddPool("magicblockdb", db)
}

func (mb *MagicBlock) Read(ctx context.Context, key string) error {
	return mb.GetEntityMetadata().GetStore().Read(ctx, key, mb)
}

func (mb *MagicBlock) Write(ctx context.Context) error {
	Logger.Info("Writing mb", zap.Int("mbnum", len(mb.AllMiners.Nodes)))
	return mb.GetEntityMetadata().GetStore().Write(ctx, mb)
}

//Delete stub for implementing the interface
func (mb *MagicBlock) Delete(ctx context.Context) error {
	//ToDo: Delete curr or next as specified by mb
	return nil
}

//SetupMagicBlock create and setup magicblock object
func SetupMagicBlock(mbType MBType, mbNumber int64, prevRS int64, startingRound int64, lastRound int64, activeSetMaxSize int, activeSetMinSize int) *MagicBlock {
	mb := &MagicBlock{}
	mb.TypeOfMB = mbType
	mb.MagicBlockNumber = mbNumber
	mb.PrevRandomSeed = prevRS
	mb.StartingRound = startingRound
	mb.EstimatedLastRound = lastRound
	mb.ActiveSetMaxSize = activeSetMaxSize
	mb.ActiveSetMinSize = activeSetMinSize
	Logger.Info("Created magic block", zap.Int64("Starting_round", mb.StartingRound), zap.String("type", fmt.Sprintf("%v", mb.TypeOfMB)), zap.Int64("ending_round", mb.EstimatedLastRound))
	return mb
}

// SetupAndInitMagicBlock given a magicblock it creates a newone and initializes all the fields appropriately.
func (mb *MagicBlock) SetupAndInitMagicBlock() (*MagicBlock, error) {
	mgc := SetupMagicBlock(mb.TypeOfMB, mb.MagicBlockNumber, mb.PrevRandomSeed, mb.StartingRound, mb.EstimatedLastRound, mb.ActiveSetMaxSize, mb.ActiveSetMinSize)

	mgc.AllMiners = node.NewPool(node.NodeTypeMiner)
	mgc.AllSharders = node.NewPool(node.NodeTypeSharder)
	mgc.ActiveSetSharders = node.NewPool(node.NodeTypeSharder)

	for _, miner := range mb.AllMiners.Nodes {
		err := mgc.AllMiners.CopyAndAddNode(miner)
		if err != nil {
			Logger.Error("Error while adding AllMiners", zap.Any("minerKey", miner.GetKey), zap.Int("minerIndex", miner.SetIndex), zap.Error(err))
			return nil, err
		}
	}
	if len(mb.AllMiners.Nodes) == 0 {
		Logger.Panic("could not load Allminers")
	}
	mgc.AllMiners.ComputeProperties()

	//ToDo: Big assumption that DKGset rules have not been changed since first it was created
	_, err := mgc.GetComputedDKGSet() //this takes care of populating DKGset also
	if err != nil {
		Logger.Error("Error while adding DkgSetMiners", zap.Error(err))
		return nil, err
	}

	//This takes care creating and populating activeset
	mgc.DkgDone(mb.SecretKeyGroupStr, mb.RandomSeed)

	for _, sharder := range mb.AllSharders.Nodes {
		err := mgc.AllSharders.CopyAndAddNode(sharder)
		if err != nil {
			Logger.Error("Error while adding AllSharders", zap.Any("sharderKey", sharder.GetKey), zap.Int("sharderIndex", sharder.SetIndex), zap.Error(err))
			return nil, err
		}
	}
	//ToDo: Until we've sharders onboarding this should suffice
	mgc.AllSharders.ComputeProperties()
	for _, sharder := range mgc.AllSharders.Nodes {
		mgc.ActiveSetSharders.AddNode(sharder)
	}

	mgc.ActiveSetSharders.ComputeProperties()
	mgc.ComputeMinerRanks(mgc.ActiveSetMiners)
	Logger.Info("new mb info", zap.Int("allMinersNum", len(mgc.AllMiners.Nodes)), zap.Int("activesetMinersNum", len(mgc.ActiveSetMiners.Nodes)), zap.Int("activesetShardersNum", len(mgc.ActiveSetSharders.Nodes)))

	return mgc, nil
}

// SetupNextMagicBlock setup the next including allminers and activeset sharders
func (mb *MagicBlock) SetupNextMagicBlock() (*MagicBlock, error) {
	c := GetServerChain()
	nextMgc := SetupMagicBlock(NEXT, mb.MagicBlockNumber+1, mb.RandomSeed, mb.EstimatedLastRound+1, CalcLastRound(mb.EstimatedLastRound+1, c.MagicBlockLife), mb.ActiveSetMaxSize, mb.ActiveSetMinSize)
	nextMgc.AllMiners = node.NewPool(node.NodeTypeMiner)
	nextMgc.AllSharders = node.NewPool(node.NodeTypeSharder)
	nextMgc.ActiveSetSharders = node.NewPool(node.NodeTypeSharder)
	for _, miner := range mb.AllMiners.Nodes {
		nextMgc.AllMiners.AddNode(miner)
	}
	nextMgc.AllMiners.ComputeProperties()

	np, err := nextMgc.GetComputedDKGSet()

	if err != nil {
		return nil, err
	}
	nextMgc.DKGSetMiners = np
	//ToDo: Until we've sharders onboarding this should suffice
	for _, sharder := range mb.AllSharders.Nodes {
		nextMgc.AllSharders.AddNode(sharder)
		nextMgc.ActiveSetSharders.AddNode(sharder)
	}

	nextMgc.AllSharders.ComputeProperties()
	nextMgc.ActiveSetSharders.ComputeProperties()
	Logger.Info("next mb info", zap.Int("len_of_miners", len(nextMgc.AllMiners.Nodes)), zap.Int("len_of_sharders", len(nextMgc.ActiveSetSharders.Nodes)))
	return nextMgc, nil
}

// AddARegisteredMiner Add the registered miner to all miners with same ID does not exist
func (mb *MagicBlock) AddARegisteredMiner(id, publicKey, hostName string, port int) {

	for _, miner := range mb.AllMiners.Nodes {
		if miner.ID == id {
			Logger.Info("AddARegisteredMiner Miner already exists", zap.String("ID", id), zap.String("hostName", hostName))
			return
		}
	}

	Logger.Info("Miner does not exist. AddingMiner is on hold", zap.String("ID", id), zap.String("hostName", hostName), zap.Int("AllMinersLen", len(mb.AllMiners.Nodes)))
	/*
		err := mb.AllMiners.CreateAndAddNode(node.NodeTypeMiner, port, hostName, hostName, id, publicKey, "")
		if err == nil {
			mb.AllMiners.ComputeProperties() //--this will change the index and lead to crash.
			Logger.Info("Miner does not exist. Added", zap.String("ID", id), zap.String("hostName", hostName), zap.Int("AllMinersLen", len(mb.AllMiners.Nodes)))
			for _, miner := range mb.AllMiners.Nodes {
				Logger.Info("AddARegisteredMiner AllMiners miner", zap.String("ID", miner.ID), zap.String("hostName", miner.Host))
			}
		} else {
			Logger.Info("Miner does not exist. failed to add", zap.String("ID", id), zap.String("hostName", hostName), zap.Error(err))

		}
	*/

}

/*ReadNodePools - read the node pools from configuration */
func (mb *MagicBlock) ReadNodePools(configFile string) error {
	nodeConfig := config.ReadConfig(configFile)
	config := nodeConfig.Get("miners")
	if miners, ok := config.([]interface{}); ok {
		if mb.AllMiners == nil {
			//Reading from config file, the node pools need to be initialized
			mb.AllMiners = node.NewPool(node.NodeTypeMiner)

			mb.AllMiners.AddNodes(miners)
			mb.AllMiners.ComputeProperties()

		}

	}
	config = nodeConfig.Get("sharders")
	if sharders, ok := config.([]interface{}); ok {
		if mb.AllSharders == nil {
			//Reading from config file, the node pools need to be initialized
			mb.AllSharders = node.NewPool(node.NodeTypeSharder)
			mb.ActiveSetSharders = node.NewPool(node.NodeTypeSharder)
			mb.AllSharders.AddNodes(sharders)
			mb.AllSharders.ComputeProperties()
			mb.ActiveSetSharders.AddNodes(sharders)
			mb.ActiveSetSharders.ComputeProperties()
		}

	}

	if mb.AllMiners == nil || mb.AllSharders == nil {
		err := common.NewError("configfile_read_err", "Either sharders or miners or both are not found in "+configFile)
		Logger.Info(err.Error())
		return err
	}
	Logger.Info("Added miners", zap.Int("all_miners", len(mb.AllMiners.Nodes)),
		zap.Int("all_sharders", len(mb.AllSharders.Nodes)),
		zap.Int("active_sharders", len(mb.ActiveSetSharders.Nodes)))

	//ToDo: NeedsFix. We need this because Sharders need this right after reading the pool. Fix it.
	mb.GetComputedDKGSet()
	return nil
}

//GetAllMiners gets all miners node pool
func (mb *MagicBlock) GetAllMiners() *node.Pool {
	return mb.AllMiners
}

//GetActiveSetMiners gets all miners in ActiveSet
func (mb *MagicBlock) GetActiveSetMiners() *node.Pool {
	return mb.ActiveSetMiners
}

//GetDkgSetMiners gets all miners participating in DKG
func (mb *MagicBlock) GetDkgSetMiners() *node.Pool {
	return mb.DKGSetMiners
}

//GetAllSharders Gets all sharders in the pool
func (mb *MagicBlock) GetAllSharders() *node.Pool {
	return mb.AllSharders
}

//GetActiveSetSharders gets all sharders in the active set
func (mb *MagicBlock) GetActiveSetSharders() *node.Pool {
	return mb.ActiveSetSharders
}

// GetComputedDKGSet select and provide miners set for DKG based on the rules
func (mb *MagicBlock) GetComputedDKGSet() (*node.Pool, *common.Error) {
	if mb.DKGSetMiners != nil {
		return mb.DKGSetMiners, nil
	}
	mb.DKGSetMiners = node.NewPool(node.NodeTypeMiner)
	miners, err := mb.getDKGSetAfterRules(mb.GetAllMiners())

	if err != nil || miners == nil {
		return miners, err
	}
	mb.DKGSetMiners = miners
	mb.DKGSetMiners.ComputeProperties()
	Logger.Info("returning computed dkg set miners", zap.Int("dkgset_num", mb.DKGSetMiners.Size()))
	return mb.DKGSetMiners, nil
}

func (mb *MagicBlock) getDKGSetAfterRules(allMiners *node.Pool) (*node.Pool, *common.Error) {
	sc := GetServerChain()

	/*
	   Rule#1: if allMiners size is less than the active set required size, you cannopt proceed
	*/
	if allMiners.Size() < sc.ActiveSetMinerMin {
		return nil, common.NewError("too_few_miners", fmt.Sprintf("Need: %v, Have %v", sc.ActiveSetMinerMin, allMiners.Size()))
	}

	var currActiveSetSize int
	if mb.ActiveSetMiners != nil {
		currActiveSetSize = mb.ActiveSetMiners.Size()
	}

	/*
	  Rule#2: DKGSet size cannot be more than increment size of the current active set size;
	  if starting, assume all miners are eligible
	*/
	var dkgSetSize int
	if currActiveSetSize > 0 {
		dkgSetSize = int(math.Ceil((float64(sc.DkgSetMinerIncMax) / 100) * float64(currActiveSetSize)))
	} else {
		dkgSetSize = allMiners.Size()
	}
	if allMiners.Size() > dkgSetSize {
		Logger.Error("Too many miners Need to use stake logic", zap.Int("need", dkgSetSize), zap.Int("have", allMiners.Size()))
	}
	dkgMiners := node.NewPool(node.NodeTypeMiner)

	for _, miner := range allMiners.Nodes {
		dkgMiners.AddNode(miner)
	}

	return dkgMiners, nil
}

// IsMbReadyForDKG are the miners in DKGSet ready for DKG
func (mb *MagicBlock) IsMbReadyForDKG() bool {
	active := mb.DKGSetMiners.GetActiveCount()
	return active >= mb.DKGSetMiners.Size()
}

// ComputeActiveSetMinersForSharder Temp API for Sharders to start with assumption that all genesys miners are active
func (mb *MagicBlock) ComputeActiveSetMinersForSharder() {
	mb.ActiveSetMiners = node.NewPool(node.NodeTypeMiner)
	//This needs more logic. Simplistic approach of all DKGSet moves to ActiveSet for now
	for _, n := range mb.DKGSetMiners.Nodes {
		mb.ActiveSetMiners.AddNode(n)
	}
	mb.ActiveSetMiners.ComputeProperties()
}

// DkgDone Tell magic block that DKG + vcvrfs is done.
func (mb *MagicBlock) DkgDone(dkgKey string, randomSeed int64) {

	mb.RandomSeed = randomSeed
	mb.SecretKeyGroupStr = dkgKey
	mb.ComputeMinerRanks(mb.DKGSetMiners)
	rankedMiners := mb.GetMinersByRank(mb.DKGSetMiners)

	Logger.Info("DkgDone computing miner ranks", zap.String("shared_key", dkgKey), zap.Int("len_of_miners", len(rankedMiners)), zap.Int("ActiveSetMaxSize", mb.ActiveSetMaxSize))
	mb.ActiveSetMiners = node.NewPool(node.NodeTypeMiner)

	for i, n := range rankedMiners {
		if mb.ActiveSetMaxSize <= i {
			break
		}
		mb.ActiveSetMiners.AddNode(n)
		Logger.Info("Adding ranked node", zap.String("ID", n.ID), zap.Int("index", i))

	}

	mb.ActiveSetMiners.ComputeProperties()
}

// AddToVcVrfSharesMap collect vrf shares for VC
func (mb *MagicBlock) AddToVcVrfSharesMap(nodeID string, share *VCVRFShare) bool {
	mb.Mutex.Lock()
	defer mb.Mutex.Unlock()
	dkgSet := mb.GetDkgSetMiners()

	//ToDo: Check if the nodeId is in dkgSet
	if mb.recVcVrfSharesMap == nil {

		mb.recVcVrfSharesMap = make(map[string]*VCVRFShare, len(dkgSet.Nodes))
	}
	if _, ok := mb.recVcVrfSharesMap[nodeID]; ok {
		Logger.Info("Ignoring VcVRF Share recived again from node : ", zap.String("Node_Id", nodeID))
		return false
	}

	mb.recVcVrfSharesMap[nodeID] = share
	return true
}

func (mb *MagicBlock) getVcVrfConsensus() int {
	/*
		Note: VcVrf is run to find the RRS that decides activeset miners right after DKG.
		It is crucial that all of them have the RRS. So, making consensus same as N.
		This means we are extending the dependency on all N nodes being present from DKG to VcVrf, which is ok.
	*/
	/*
		thresholdByCount := viper.GetInt("server_chain.block.consensus.threshold_by_count")
		return int(math.Ceil((float64(thresholdByCount) / 100) * float64(mb.GetDkgSetMiners().Size())))
	*/
	return mb.GetDkgSetMiners().Size()
}

// IsVcVrfConsensusReached --checks if there are enough VcVrf shares
func (mb *MagicBlock) IsVcVrfConsensusReached() bool {
	return len(mb.recVcVrfSharesMap) >= mb.getVcVrfConsensus()
}

// GetVcVRFShareInfo -- break down VcVRF shares to get the seed
func (mb *MagicBlock) GetVcVRFShareInfo() ([]string, []string) {
	recSig := make([]string, 0)
	recFrom := make([]string, 0)
	mb.Mutex.Lock()
	defer mb.Mutex.Unlock()

	for nodeID, share := range mb.recVcVrfSharesMap {
		recSig = append(recSig, share.Share)
		recFrom = append(recFrom, nodeID)
	}

	return recSig, recFrom
}

/*ComputeMinerRanks - Compute random order of n elements given the random seed of the round */
func (mb *MagicBlock) ComputeMinerRanks(miners *node.Pool) {
	mb.minerPerm = rand.New(rand.NewSource(mb.RandomSeed)).Perm(miners.Size())
}

// IsMinerInActiveSet checks if the given miner node is in the active set or not
func (mb *MagicBlock) IsMinerInActiveSet(miner *node.Node) bool {
	return mb.GetMinerRank(miner) <= mb.ActiveSetMaxSize
}

/*GetMinerRank - get the rank of element at the elementIdx position based on the permutation of the round */
func (mb *MagicBlock) GetMinerRank(miner *node.Node) int {
	mb.Mutex.RLock()
	defer mb.Mutex.RUnlock()
	if mb.minerPerm == nil {
		Logger.DPanic(fmt.Sprintf("miner ranks not computed yet: %v", mb.GetMagicBlockNumber()))
	}
	return mb.minerPerm[miner.SetIndex]
}

/*GetMinersByRank - get the ranks of the miners */
func (mb *MagicBlock) GetMinersByRank(miners *node.Pool) []*node.Node {
	mb.Mutex.RLock()
	defer mb.Mutex.RUnlock()
	nodes := miners.Nodes
	rminers := make([]*node.Node, len(nodes))
	for _, nd := range nodes {
		rminers[mb.minerPerm[nd.SetIndex]] = nd
	}
	return rminers
}

// GetMagicBlockNumber handy API to get the magic block number
func (mb *MagicBlock) GetMagicBlockNumber() int64 {
	return mb.MagicBlockNumber
}

// CalcLastRound calculates the last round for the view
func CalcLastRound(roundNum int64, life int64) int64 {

	mf := int64((roundNum + life) / life)
	lastRound := life * mf

	return lastRound
}