package tx_processor

import (
 "testing"
 "github.com/ethereum/go-ethereum/common"
 "github.com/ethereum/go-ethereum/crypto"
 "github.com/stretchr/testify/require"
)

func TestReviewRepeatedSelfdestructThenNestedRevert(t *testing.T) {
 t.Setenv("SKIP_MEMPOOL_SIG_VERIFY","false")
 t.Setenv("METANODE_DEVNET","false")
 key,err:=crypto.GenerateKey();require.NoError(t,err)
 w:=&confWorld{sender:crypto.PubkeyToAddress(key.PublicKey)}
 for i:=0;i<confContracts;i++ {w.p=append(w.p,crypto.CreateAddress(w.sender,uint64(1+i)))}
 w.eoas=[]common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111"),common.HexToAddress("0x2222222222222222222222222222222222222222")}
 w.inits=confChildInits(w.p)
 child:=crypto.CreateAddress(w.p[0],1)
 childInit:=confDeployInit(confScript(w.eoas[0],"SELFDESTRUCT"))
 for _,revert:=range []bool{false,true} {
  name:="successful_proxy_control";if revert {name="reverted_proxy"}
  t.Run(name,func(t *testing.T){
   // Destroy child once, then let a proxy call it again. Reverting the
   // proxy must preserve the first successful destruction's pending marker.
   body:=confJoin(confMemWrite(0,childInit),[]any{len(childInit),0,100,"CREATE","POP"},confCallSeq("CALL",child,0,0),[]any{"POP"})
   body=confJoin(body,confCallSeq("CALL",w.p[1],0,0),[]any{"POP"})
   body=append(body,"STOP")
   proxy:=confJoin(confCallSeq("CALL",child,0,0),[]any{"POP"})
   if revert {proxy=append(proxy,0,0,"REVERT")} else {proxy=append(proxy,"STOP")}
   runtimes:=[][]byte{confScript(body...),confScript(proxy...),{0},{0}}
   confMVMGas=2_000_000
   g:=confRunGeth(w,runtimes,[]int64{200,0,0,0},0,nil)
   m:=confRunMVMReal(t,w,key,runtimes,[]int64{200,0,0,0},0,nil)
   require.True(t,g.ok,g.failKind);require.True(t,m.ok,m.failKind)
   t.Logf("proxy_reverts=%v geth child_has_code=%v MVM child_has_code=%v; geth recipient=%s MVM recipient=%s",revert,g.hasCode[child],m.hasCode[child],g.balance[w.eoas[0]],m.balance[w.eoas[0]])
   require.Empty(t,confCompare(w,g,m))
  })
 }
}
