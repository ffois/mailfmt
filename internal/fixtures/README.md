# Fixture

I file in `testdata/` sono derivati da messaggi reali, non copiati.

Da ogni messaggio di partenza è stata mantenuta la struttura MIME — confini,
header di parte, codifiche, ordine delle parti — e sostituito tutto il resto:

- nomi, indirizzi email, numeri di telefono, codici fiscali e numeri
  identificativi rimpiazzati con valori inventati;
- corpi dei messaggi riscritti;
- allegati troncati a pochi byte, mantenendo header e codifica.

I messaggi di partenza contengono dati personali e non sono versionati.

I fixture `.msg` in `testdata/msg/` non sono derivati: sono generati dal
test `TestGenerateMSGTestdata` in `internal/msg/gen_testdata_test.go`, che si
appoggia al writer CFB in `internal/msg/cfbwriter_test.go`, perché un
compound file OLE2 non si anonimizza in modo affidabile con una sostituzione
testuale. Per rigenerarli:
`go test ./internal/msg/ -run TestGenerateMSGTestdata -update`.
