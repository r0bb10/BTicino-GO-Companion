package media

import "errors"

// ErrSIPCallInProgress reports that the device refused an outgoing INVITE with
// 486 Busy Here. The AV pipeline is already active, so stream setup can still
// proceed by adding the RTP destination through bt_ipcamera.
var ErrSIPCallInProgress = errors.New("sip call already in progress (486)")
