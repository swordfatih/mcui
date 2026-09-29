package app

import (
 "encoding/json"
 "os"
 "testing"
)

// Rebuild with MCUI_REFERENCE_SOURCE pointing at resource_pack from the pinned
// official sample revision documented in content_reference/README.md.
func TestGenerateContentReference(t *testing.T) {
 source:=os.Getenv("MCUI_REFERENCE_SOURCE");if source=="" {t.Skip("reference generation is opt-in")}
 graph,warnings,err:=scanContent(source);if err!=nil {t.Fatal(err)}
 resolveContentRefs(graph,nil)
 needed:=map[string]bool{};for id:=range graph {if contentCategory(id)!="" {for dep:=range contentClosure(graph,id){needed[dep]=true}}}
 refs:=map[string][]string{};for id:=range needed {refs[id]=[]string{};if n:=graph[id];n!=nil {refs[id]=uniqueContentStrings(n.Refs)}}
 data,err:=json.Marshal(refs);if err!=nil{t.Fatal(err)}
 if err:=os.WriteFile("content_reference/vanilla.json",append(data,'\n'),0644);err!=nil{t.Fatal(err)}
 t.Logf("%d reference nodes; %d unreadable JSON files",len(refs),len(warnings))
}
