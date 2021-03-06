package co

import (
	"encoding/binary"
	"fmt"
	"log"
	"seal/rtmp/pt"

	"github.com/calabashdad/utiltools"
)

// recvMsg recv whole msg and quit when got an entire msg, not handle it at all.
func (rc *RtmpConn) recvMsg(header *pt.MessageHeader, payload *pt.MessagePayload) (err error) {
	defer func() {
		if err := recover(); err != nil {
			log.Println(utiltools.PanicTrace())
		}
	}()

	for {
		// read basic header
		var buf [3]uint8
		if err = rc.tcpConn.ExpectBytesFull(buf[:1], 1); err != nil {
			return
		}

		chunkFmt := (buf[0] & 0xc0) >> 6
		csid := uint32(buf[0] & 0x3f)

		switch csid {
		case 0:
			//csId 2 bytes. 64-319
			err = rc.tcpConn.ExpectBytesFull(buf[1:2], 1)
			csid = uint32(64 + buf[1])
		case 1:
			//csId 3 bytes. 64-65599
			err = rc.tcpConn.ExpectBytesFull(buf[1:3], 2)
			csid = uint32(64) + uint32(buf[1]) + uint32(buf[2])*256
		}

		if err != nil {
			break
		}

		chunk, ok := rc.chunkStreams[csid]
		if !ok {
			chunk = &pt.ChunkStream{}
			rc.chunkStreams[csid] = chunk
		}

		chunk.Fmt = chunkFmt
		chunk.CsID = csid
		chunk.MsgHeader.PerferCsid = csid

		// read message header
		if 0 == chunk.MsgCount && chunk.Fmt != pt.RtmpFmtType0 {
			if pt.RtmpCidProtocolControl == chunk.CsID && pt.RtmpFmtType1 == chunk.Fmt {
				// for librtmp, if ping, it will send a fresh stream with fmt=1,
				// 0x42             where: fmt=1, cid=2, protocol contorl user-control message
				// 0x00 0x00 0x00   where: timestamp=0
				// 0x00 0x00 0x06   where: payload_length=6
				// 0x04             where: message_type=4(protocol control user-control message)
				// 0x00 0x06            where: event Ping(0x06)
				// 0x00 0x00 0x0d 0x0f  where: event data 4bytes ping timestamp.
				log.Println("rtmp session, accept cid=2, chunkFmt=1 , it's a valid chunk format, for librtmp.")
			} else {
				err = fmt.Errorf("chunk start error, must be RTMP_FMT_TYPE0")
				break
			}
		}

		if payload.SizeTmp > 0 && pt.RtmpFmtType0 == chunk.Fmt {
			err = fmt.Errorf("when msg count > 0, chunk fmt is not allowed to be RTMP_FMT_TYPE0")
			break
		}

		var msgHeaderSize uint32

		switch chunk.Fmt {
		case pt.RtmpFmtType0:
			msgHeaderSize = 11
		case pt.RtmpFmtType1:
			msgHeaderSize = 7
		case pt.RtmpFmtType2:
			msgHeaderSize = 3
		case pt.RtmpFmtType3:
			msgHeaderSize = 0
		}

		var msgHeader [11]uint8 //max is 11
		err = rc.tcpConn.ExpectBytesFull(msgHeader[:], msgHeaderSize)
		if err != nil {
			break
		}

		// parse msg header
		// 3bytes: timestamp delta,    fmt=0,1,2
		// 3bytes: payload length,     fmt=0,1
		// 1bytes: message type,       fmt=0,1
		// 4bytes: stream id,          fmt=0
		switch chunk.Fmt {
		case pt.RtmpFmtType0:
			chunk.MsgHeader.TimestampDelta = uint32(msgHeader[0])<<16 + uint32(msgHeader[1])<<8 + uint32(msgHeader[2])
			if chunk.MsgHeader.TimestampDelta >= pt.RtmpExtendTimeStamp {
				chunk.HasExtendedTimestamp = true
			} else {
				chunk.HasExtendedTimestamp = false
				// For a type-0 chunk, the absolute timestamp of the message is sent here.
				chunk.MsgHeader.Timestamp = uint64(chunk.MsgHeader.TimestampDelta)
			}

			payloadLength := uint32(msgHeader[3])<<16 + uint32(msgHeader[4])<<8 + uint32(msgHeader[5])
			if payload.SizeTmp > 0 && payloadLength != chunk.MsgHeader.PayloadLength {
				err = fmt.Errorf("RTMP_FMT_TYPE0: msg has in chunk, msg size can not change")
				break
			}

			chunk.MsgHeader.PayloadLength = payloadLength
			chunk.MsgHeader.MessageType = msgHeader[6]
			chunk.MsgHeader.StreamID = binary.LittleEndian.Uint32(msgHeader[7:11])

		case pt.RtmpFmtType1:
			chunk.MsgHeader.TimestampDelta = uint32(msgHeader[0])<<16 + uint32(msgHeader[1])<<8 + uint32(msgHeader[2])
			if chunk.MsgHeader.TimestampDelta >= pt.RtmpExtendTimeStamp {
				chunk.HasExtendedTimestamp = true
			} else {
				chunk.HasExtendedTimestamp = false
				chunk.MsgHeader.Timestamp += uint64(chunk.MsgHeader.TimestampDelta)
			}

			payloadLength := uint32(msgHeader[3])<<16 + uint32(msgHeader[4])<<8 + uint32(msgHeader[5])
			if payload.SizeTmp > 0 && payloadLength != chunk.MsgHeader.PayloadLength {
				err = fmt.Errorf("RTMP_FMT_TYPE1: msg has in chunk, msg size can not change")
				break
			}

			chunk.MsgHeader.PayloadLength = payloadLength
			chunk.MsgHeader.MessageType = msgHeader[6]

		case pt.RtmpFmtType2:
			chunk.MsgHeader.TimestampDelta = uint32(msgHeader[0])<<16 + uint32(msgHeader[1])<<8 + uint32(msgHeader[2])
			if chunk.MsgHeader.TimestampDelta >= pt.RtmpExtendTimeStamp {
				chunk.HasExtendedTimestamp = true
			} else {
				chunk.HasExtendedTimestamp = false
				chunk.MsgHeader.Timestamp += uint64(chunk.MsgHeader.TimestampDelta)
			}
		case pt.RtmpFmtType3:
			// update the timestamp even fmt=3 for first chunk packet. the same with previous.
			if 0 == payload.SizeTmp && !chunk.HasExtendedTimestamp {
				chunk.MsgHeader.Timestamp += uint64(chunk.MsgHeader.TimestampDelta)
			}
		}

		if err != nil {
			break
		}

		// read extend timestamp
		if chunk.HasExtendedTimestamp {
			var buf [4]uint8
			if err = rc.tcpConn.ExpectBytesFull(buf[:], 4); err != nil {
				break
			}

			extendTimeStamp := binary.BigEndian.Uint32(buf[0:4])

			// always use 31bits timestamp, for some server may use 32bits extended timestamp.
			extendTimeStamp &= 0x7fffffff

			chunkTimeStamp := chunk.MsgHeader.Timestamp
			if 0 == payload.SizeTmp || 0 == chunkTimeStamp {
				chunk.MsgHeader.Timestamp = uint64(extendTimeStamp)
			}

			// because of the flv file format is lower 24bits, and higher 8bit is SI32, so timestamp is 31bit.
			chunk.MsgHeader.Timestamp &= 0x7fffffff
		}

		chunk.MsgCount++

		// make cache of msg
		if uint32(len(payload.Payload)) < chunk.MsgHeader.PayloadLength {
			payload.Payload = make([]uint8, chunk.MsgHeader.PayloadLength)
		}

		// read chunk data
		remainPayloadSize := chunk.MsgHeader.PayloadLength - payload.SizeTmp

		if remainPayloadSize >= rc.inChunkSize {
			remainPayloadSize = rc.inChunkSize
		}

		if err = rc.tcpConn.ExpectBytesFull(payload.Payload[payload.SizeTmp:payload.SizeTmp+remainPayloadSize], remainPayloadSize); err != nil {
			break
		} else {
			payload.SizeTmp += remainPayloadSize
			if payload.SizeTmp == chunk.MsgHeader.PayloadLength {

				*header = chunk.MsgHeader

				// has recv entire rtmp message.
				// reset the payload size this time, the message actually size is header length, this chunk can reuse by a new csid.
				payload.SizeTmp = 0

				break
			}
		}

	}

	if err != nil {
		return
	}

	return
}
