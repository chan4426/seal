package seal_rtmp_conn

import (
	"UtilsTools/identify_panic"
	"log"
)

func (rtmp *RtmpConn) handlePlayLoop() (err error) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(err, ",panic at,", identify_panic.IdentifyPanic())
		}
	}()

	return
}
