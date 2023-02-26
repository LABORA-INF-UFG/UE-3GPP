package handler

import (
	"UE-non3GPP/internal/ike/context"
	"UE-non3GPP/internal/ike/message"
	"UE-non3GPP/internal/ipsec"
	"UE-non3GPP/internal/nas/dispatch"
	messageNas "UE-non3GPP/internal/nas/message"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
)

func HandleIKESAINIT(ue *context.UeIke, ikeMsg *message.IKEMessage) {

	// handle IKE SA INIT Response
	var securityAssociation *message.SecurityAssociation
	var notifications []*message.Notification
	var sharedKeyData []byte
	var remoteNonce []byte
	var encryptionAlgorithmTransform, pseudorandomFunctionTransform *message.Transform
	var integrityAlgorithmTransform, diffieHellmanGroupTransform *message.Transform

	if ikeMsg.Flags != message.ResponseBitCheck {
		// TODO handle errors in ike header
		return
	}

	// recover UE information based in parameters of ike message
	for _, ikePayload := range ikeMsg.Payloads {
		switch ikePayload.Type() {
		case message.TypeSA:
			securityAssociation = ikePayload.(*message.SecurityAssociation)
		case message.TypeKE:
			remotePublicKeyExchangeValue := ikePayload.(*message.KeyExchange).KeyExchangeData
			var i int = 0
			for {
				if remotePublicKeyExchangeValue[i] != 0 {
					break
				}
			}
			remotePublicKeyExchangeValue = remotePublicKeyExchangeValue[i:]
			remotePublicKeyExchangeValueBig := new(big.Int).
				SetBytes(remotePublicKeyExchangeValue)
			sharedKeyData = new(big.Int).Exp(remotePublicKeyExchangeValueBig,
				ue.GetSecret(), ue.GetFactor()).Bytes()
		case message.TypeNiNr:
			remoteNonce = ikePayload.(*message.Nonce).NonceData
		case message.TypeN:
			notifications = append(notifications, ikePayload.(*message.Notification))
		default:
			// TODO handle in ike payloads
		}
	}

	// retrieve client context
	if securityAssociation != nil {

		for _, proposal := range securityAssociation.Proposals {
			// We need ENCR, PRF, INTEG, DH
			encryptionAlgorithmTransform = nil
			pseudorandomFunctionTransform = nil
			integrityAlgorithmTransform = nil
			diffieHellmanGroupTransform = nil

			if len(proposal.EncryptionAlgorithm) > 0 {
				for _, transform := range proposal.EncryptionAlgorithm {
					if transform.TransformID == ue.GetEncryptionAlgoritm() {
						encryptionAlgorithmTransform = transform
						break
					}
				}
				if encryptionAlgorithmTransform == nil {
					continue
				}
			} else {
				continue // mandatory
			}

			if len(proposal.PseudorandomFunction) > 0 {
				for _, transform := range proposal.PseudorandomFunction {
					if transform.TransformID == ue.GetPseudorandomFunction() {
						pseudorandomFunctionTransform = transform
						break
					}
				}
				if pseudorandomFunctionTransform == nil {
					continue
				}
			} else {
				continue // mandatory
			}

			if len(proposal.IntegrityAlgorithm) > 0 {
				for _, transform := range proposal.IntegrityAlgorithm {
					if transform.TransformID == ue.GetIntegrityAlgorithm() {
						integrityAlgorithmTransform = transform
						break
					}
				}
				if integrityAlgorithmTransform == nil {
					continue
				}
			} else {
				continue // mandatory
			}

			if len(proposal.DiffieHellmanGroup) > 0 {
				for _, transform := range proposal.DiffieHellmanGroup {
					if transform.TransformID == ue.GetDiffieHellmanGroup() {
						diffieHellmanGroupTransform = transform
						break
					}
				}
				if diffieHellmanGroupTransform == nil {
					continue
				}
			} else {
				continue // mandatory
			}

		}
	}

	ikeSecurityAssociation := &context.IKESecurityAssociation{
		LocalSPI:               ikeMsg.InitiatorSPI,
		RemoteSPI:              ikeMsg.ResponderSPI,
		InitiatorMessageID:     ikeMsg.MessageID,
		ResponderMessageID:     ikeMsg.MessageID,
		EncryptionAlgorithm:    encryptionAlgorithmTransform,
		IntegrityAlgorithm:     integrityAlgorithmTransform,
		PseudorandomFunction:   pseudorandomFunctionTransform,
		DiffieHellmanGroup:     diffieHellmanGroupTransform,
		ConcatenatedNonce:      append(ue.GetLocalNonce(), remoteNonce...),
		DiffieHellmanSharedKey: sharedKeyData,
	}

	if err := context.GenerateKeyForIKESA(ikeSecurityAssociation); err != nil {
		// TODO handle errors
		return
	}

	// create ike security assocation
	ue.CreateN3IWFIKESecurityAssociation(ikeSecurityAssociation)

	// send IKE_AUTH
	responseIKEMessage := new(message.IKEMessage)

	ue.N3IWFIKESecurityAssociation.InitiatorMessageID++

	responseIKEMessage.BuildIKEHeader(
		ue.N3IWFIKESecurityAssociation.LocalSPI, ue.N3IWFIKESecurityAssociation.RemoteSPI,
		message.IKE_AUTH, message.InitiatorBitCheck,
		ue.N3IWFIKESecurityAssociation.InitiatorMessageID)

	var ikePayload message.IKEPayloadContainer

	// Identification
	ikePayload.BuildIdentificationInitiator(message.ID_FQDN, []byte("UE"))

	// Security Association
	securityAssociation = ikePayload.BuildSecurityAssociation()

	var attributeType uint16 = message.AttributeTypeKeyLength
	var keyLength uint16 = 256

	// Proposal 1
	inboundSPI := ue.GenerateSPI()

	proposal := securityAssociation.Proposals.BuildProposal(1,
		message.TypeESP, inboundSPI)
	// ENCR
	proposal.EncryptionAlgorithm.BuildTransform(message.TypeEncryptionAlgorithm,
		ue.GetEncryptionAlgoritm(), &attributeType, &keyLength, nil)
	// INTEG
	proposal.IntegrityAlgorithm.BuildTransform(message.TypeIntegrityAlgorithm,
		ue.GetIntegrityAlgorithm(), nil, nil,
		nil)
	// ESN
	proposal.ExtendedSequenceNumbers.BuildTransform(message.TypeExtendedSequenceNumbers,
		message.ESN_NO, nil, nil, nil)

	// Traffic Selector
	tsi := ikePayload.BuildTrafficSelectorInitiator()
	tsi.TrafficSelectors.BuildIndividualTrafficSelector(message.TS_IPV4_ADDR_RANGE,
		0, 0, 65535,
		[]byte{0, 0, 0, 0}, []byte{255, 255, 255, 255})
	tsr := ikePayload.BuildTrafficSelectorResponder()
	tsr.TrafficSelectors.BuildIndividualTrafficSelector(message.TS_IPV4_ADDR_RANGE,
		0, 0, 65535, []byte{0, 0, 0, 0},
		[]byte{255, 255, 255, 255})

	if err := context.EncryptProcedure(ue.N3IWFIKESecurityAssociation, ikePayload,
		responseIKEMessage); err != nil {
		// TODO handle errors
		return
	}

	// Send to N3IWF
	ikeMessageData, err := responseIKEMessage.Encode()
	if err != nil {
		// TODO handle errors
		return
	}
	udp := ue.GetUdpConn()
	_, err = udp.Write(ikeMessageData)
	if err != nil {
		// TODO handle errors
		return
	}

	ue.CreateHalfChildSA(
		ue.N3IWFIKESecurityAssociation.InitiatorMessageID,
		binary.BigEndian.Uint32(inboundSPI),
		-1)

}

const (
	PreSignalling = iota
	EAPSignalling
	PostSignalling
	TCPEstablishSignalling
)

func HandleIKEAUTH(ue *context.UeIke, ikeMsg *message.IKEMessage) {

	var encryptedPayload *message.Encrypted

	if ikeMsg.Flags != message.ResponseBitCheck {
		// TODO handle errors in IKE header
		return
	}

	localSPI := ikeMsg.ResponderSPI
	if localSPI != ue.N3IWFIKESecurityAssociation.RemoteSPI {
		// TODO handle errors in IKE header
		return
	}

	for _, ikePayload := range ikeMsg.Payloads {
		switch ikePayload.Type() {
		case message.TypeSK:
			encryptedPayload = ikePayload.(*message.Encrypted)
		default:
			return
		}
	}

	decryptedIKEPayload, err := context.DecryptProcedure(ue.N3IWFIKESecurityAssociation,
		ikeMsg, encryptedPayload)
	if err != nil {
		// TODO handle errors in IKE header
		return
	}

	var eap *message.EAP
	var securityAssociation *message.SecurityAssociation
	var trafficSelectorInitiator *message.TrafficSelectorInitiator
	var trafficSelectorResponder *message.TrafficSelectorResponder
	var configuration *message.Configuration
	var notifications []*message.Notification

	for _, ikePayload := range decryptedIKEPayload {
		switch ikePayload.Type() {
		case message.TypeIDi:
			_ = ikePayload.(*message.IdentificationInitiator)
		case message.TypeCERTreq:
			_ = ikePayload.(*message.CertificateRequest)
		case message.TypeCERT:
			_ = ikePayload.(*message.Certificate)
		case message.TypeSA:
			securityAssociation = ikePayload.(*message.SecurityAssociation)
		case message.TypeTSi:
			trafficSelectorInitiator = ikePayload.(*message.TrafficSelectorInitiator)
		case message.TypeTSr:
			trafficSelectorResponder = ikePayload.(*message.TrafficSelectorResponder)
		case message.TypeEAP:
			eap = ikePayload.(*message.EAP)
		case message.TypeAUTH:
			_ = ikePayload.(*message.Authentication)
		case message.TypeCP:
			configuration = ikePayload.(*message.Configuration)
		case message.TypeN:
			notifications = append(notifications, ikePayload.(*message.Notification))
		default:
			// TODO handle errors in IKE header
		}
	}

	// completes the EAP-5G session
	if eap != nil && eap.Code == message.EAPCodeSuccess {
		// change the IKE state to EAP signalling
		ue.N3IWFIKESecurityAssociation.State++
	}

	var ikePayload message.IKEPayloadContainer
	var responseIKEMessage *message.IKEMessage

	responseIKEMessage = new(message.IKEMessage)

	switch ue.N3IWFIKESecurityAssociation.State {

	case PreSignalling:

		// IKE_AUTH - EAP exchange
		ue.N3IWFIKESecurityAssociation.InitiatorMessageID++

		responseIKEMessage.BuildIKEHeader(
			ue.N3IWFIKESecurityAssociation.LocalSPI,
			ue.N3IWFIKESecurityAssociation.RemoteSPI,
			message.IKE_AUTH, message.InitiatorBitCheck,
			ue.N3IWFIKESecurityAssociation.InitiatorMessageID)

		// EAP-5G vendor type data
		//TODO duplicate code
		eapVendorTypeData := make([]byte, 2)
		eapVendorTypeData[0] = message.EAP5GType5GNAS

		// AN Parameters
		// TODO Hardcode snssai, mcc, mnc and guami information
		anParameters := message.BuildEAP5GANParameters()
		anParametersLength := make([]byte, 2)
		binary.BigEndian.PutUint16(anParametersLength, uint16(len(anParameters)))
		eapVendorTypeData = append(eapVendorTypeData, anParametersLength...)
		eapVendorTypeData = append(eapVendorTypeData, anParameters...)

		// Send Registration Request
		// create context for NAS signal
		registrationRequest := messageNas.BuildRegistrationRequest(ue.NasContext)
		nasLength := make([]byte, 2)
		binary.BigEndian.PutUint16(nasLength, uint16(len(registrationRequest)))
		eapVendorTypeData = append(eapVendorTypeData, nasLength...)
		eapVendorTypeData = append(eapVendorTypeData, registrationRequest...)

		// EAP
		eap := ikePayload.BuildEAP(message.EAPCodeResponse, eap.Identifier)
		eap.EAPTypeData.BuildEAPExpanded(message.VendorID3GPP, message.VendorTypeEAP5G,
			eapVendorTypeData)
		if err := context.EncryptProcedure(ue.N3IWFIKESecurityAssociation, ikePayload,
			responseIKEMessage); err != nil {
			// TODO handle errors
			return
		}

		// change the IKE state to EAP signalling
		ue.N3IWFIKESecurityAssociation.State++

		// Send to N3IWF
		ikeMessageData, err := responseIKEMessage.Encode()
		if err != nil {
			// TODO handle errors
			return
		}
		udp := ue.GetUdpConn()
		_, err = udp.Write(ikeMessageData)
		if err != nil {
			// TODO handle errors
			return
		}

	case EAPSignalling:

		// receive EAP/NAS messages
		// get NAS data
		eapExpanded, ok := eap.EAPTypeData[0].(*message.EAPExpanded)
		if !ok {
			// TODO handle errors in IKE header
			return
		}
		nasData := eapExpanded.VendorData[4:]

		// handle NAS message
		responseNas, error := dispatch.DispatchNas(nasData, ue.NasContext)
		if error != nil {
			// TODO handle errors in IKE header
			return
		}

		ue.N3IWFIKESecurityAssociation.InitiatorMessageID++

		responseIKEMessage.BuildIKEHeader(
			ue.N3IWFIKESecurityAssociation.LocalSPI,
			ue.N3IWFIKESecurityAssociation.RemoteSPI,
			message.IKE_AUTH, message.InitiatorBitCheck,
			ue.N3IWFIKESecurityAssociation.InitiatorMessageID,
		)

		// EAP-5G vendor type data
		eapVendorTypeData := make([]byte, 4)
		eapVendorTypeData[0] = message.EAP5GType5GNAS

		// NAS messages
		nasLength := make([]byte, 2)
		binary.BigEndian.PutUint16(nasLength, uint16(len(responseNas)))
		eapVendorTypeData = append(eapVendorTypeData, nasLength...)
		eapVendorTypeData = append(eapVendorTypeData, responseNas...)

		// EAP
		eap := ikePayload.BuildEAP(message.EAPCodeResponse, eap.Identifier)
		eap.EAPTypeData.BuildEAPExpanded(message.VendorID3GPP, message.VendorTypeEAP5G,
			eapVendorTypeData)
		if err := context.EncryptProcedure(ue.N3IWFIKESecurityAssociation,
			ikePayload, responseIKEMessage); err != nil {
			// TODO handle errors
			return
		}

		// Send to N3IWF
		ikeMessageData, err := responseIKEMessage.Encode()
		if err != nil {
			// TODO handle errors
			return
		}
		udp := ue.GetUdpConn()
		_, err = udp.Write(ikeMessageData)
		if err != nil {
			// TODO handle errors
			return
		}

	case PostSignalling:

		// handling establishment of the IPsec tunnel
		ue.N3IWFIKESecurityAssociation.InitiatorMessageID++

		responseIKEMessage.BuildIKEHeader(ue.N3IWFIKESecurityAssociation.LocalSPI,
			ue.N3IWFIKESecurityAssociation.RemoteSPI,
			message.IKE_AUTH, message.InitiatorBitCheck,
			ue.N3IWFIKESecurityAssociation.InitiatorMessageID)

		// Authentication
		ikePayload.BuildAuthentication(message.SharedKeyMesageIntegrityCode,
			[]byte{1, 2, 3})

		// Configuration Request
		configurationRequest := ikePayload.BuildConfiguration(message.CFG_REQUEST)
		configurationRequest.ConfigurationAttribute.BuildConfigurationAttribute(
			message.INTERNAL_IP4_ADDRESS,
			nil)

		err = context.EncryptProcedure(ue.N3IWFIKESecurityAssociation,
			ikePayload, responseIKEMessage)
		if err != nil {
			// TODO handle errors
			return
		}

		// Send to N3IWF
		ikeMessageData, err := responseIKEMessage.Encode()
		if err != nil {
			// TODO handle errors
			return
		}
		udp := ue.GetUdpConn()
		_, err = udp.Write(ikeMessageData)
		if err != nil {
			// TODO handle errors
			return
		}

		// change the IKE state to TCPEstablishSignalling
		ue.N3IWFIKESecurityAssociation.State++

	case TCPEstablishSignalling:

		// N3IWF TCP Ip/Port
		n3iwfNASAddr := new(net.TCPAddr)
		var ueInnerAddrIp []byte
		var ueInnerAddrMask []byte

		// security association
		ue.N3IWFIKESecurityAssociation.IKEAuthResponseSA = securityAssociation

		// notification
		for j := 0; j < len(notifications); j++ {
			if notifications[j].NotifyMessageType == message.Vendor3GPPNotifyTypeNAS_IP4_ADDRESS {
				n3iwfNASAddr.IP = net.IPv4(
					notifications[j].NotificationData[0],
					notifications[j].NotificationData[1],
					notifications[j].NotificationData[2],
					notifications[j].NotificationData[3])
			}

			if notifications[j].NotifyMessageType == message.Vendor3GPPNotifyTypeNAS_TCP_PORT {
				n3iwfNASAddr.Port = int(
					binary.BigEndian.Uint16(notifications[j].NotificationData))
			}
		}

		if configuration.ConfigurationType == message.CFG_REPLY {
			for _, configAttr := range configuration.ConfigurationAttribute {
				if configAttr.Type == message.INTERNAL_IP4_ADDRESS {
					ueInnerAddrIp = configAttr.Value
				}
				if configAttr.Type == message.INTERNAL_IP4_NETMASK {
					ueInnerAddrMask = configAttr.Value
				}
			}
		}

		OutboundSPI := binary.BigEndian.Uint32(ue.N3IWFIKESecurityAssociation.
			IKEAuthResponseSA.Proposals[0].SPI)

		childSecurityAssociationContext, err := ue.CompleteChildSA(
			0x01, OutboundSPI,
			ue.N3IWFIKESecurityAssociation.IKEAuthResponseSA)
		if err != nil {
			return
		}

		err = context.ParseIPAddressInformationToChildSecurityAssociation(
			childSecurityAssociationContext,
			trafficSelectorInitiator.TrafficSelectors[0],
			trafficSelectorResponder.TrafficSelectors[0],
			ue)
		if err != nil {
			return
		}

		if err := context.GenerateKeyForChildSA(
			ue.N3IWFIKESecurityAssociation,
			childSecurityAssociationContext); err != nil {
			return
		}

		// thread with Tcp/XFRM connection
		go ipsec.Run(ueInnerAddrIp,
			ueInnerAddrMask,
			childSecurityAssociationContext,
			n3iwfNASAddr,
			ue.NasContext)

	default:
		return
	}
}

func HandleCREATECHILDSA(ue *context.UeIke, ikeMsg *message.IKEMessage) {
	fmt.Println(ue)
	fmt.Println(ikeMsg)
}
