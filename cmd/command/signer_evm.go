package command

import (
	"context"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ethereum/go-ethereum"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/urfave/cli/v2"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	"github.com/mocachain/moca-storage-provider/cmd/utils"
	"github.com/mocachain/moca-storage-provider/modular/signer"
	"github.com/mocachain/moca-storage-provider/store/sqldb"
	"github.com/mocachain/moca-storage-provider/util"
)

const signerEvmCommands = "SIGNER EVM STATE COMMANDS"

// Exit codes of the signer-evm commands.
const (
	signerEvmExitError        = 1
	signerEvmExitNotSatisfied = 2
)

var signerEvmJSONFlag = &cli.BoolFlag{
	Name:  "json",
	Usage: "Print the report as JSON",
}

var signerEvmHashFlag = &cli.StringSliceFlag{
	Name:  "hash",
	Usage: "An EVM transaction hash from the old signer's logs that must have a receipt; repeat for each hash",
}

var signerEvmAddressFlag = &cli.StringSliceFlag{
	Name: "address",
	Usage: "A signing account to check instead of the one derived from its configured key, as scope=0xADDRESS " +
		"(scope is operator, seal or gc); repeat for more scopes. Every scope is always checked",
}

var signerEvmBlockWaitFlag = &cli.DurationFlag{
	Name:  "block-wait",
	Usage: "How long to wait for the second sampling height",
	Value: 2 * time.Minute,
}

// SignerEvmCmd groups the commands on the signer's persisted EVM state.
var SignerEvmCmd = &cli.Command{
	Name:     "signer-evm",
	Usage:    "Inspect and maintain the signer's persisted EVM submission state",
	Category: signerEvmCommands,
	Subcommands: []*cli.Command{
		{
			Name:  "migrate",
			Usage: "Create the four signer EVM tables (run with the migration account)",
			Description: `Creates signer_evm_signed_tx, signer_evm_state, signer_evm_lease and ` +
				`signer_evm_audit if they do not exist, then checks them as the signer does on start. The signer never ` +
				`creates them itself. SP_DB_USER/SP_DB_PASSWORD/SP_DB_ADDRESS/SP_DB_DATABASE override the [SpDB] config.`,
			Action: signerEvmMigrateAction,
		},
		{
			Name:  "list",
			Usage: "Read-only: pending rows, active outcomes, the lease, invalid evidence and chain state",
			Description: `Lists the signer EVM state of the configured chain. Every row is checked against its signed ` +
				`transaction evidence; rows that fail are marked. For pending rows the receipt and the sender's ` +
				`nonce are read from the configured EVM RPC. Exit code 2 when any row has invalid evidence.`,
			Flags:  []cli.Flag{signerEvmJSONFlag},
			Action: signerEvmListAction,
		},
		{
			Name:  "preflight",
			Usage: "Read-only: check that the old signer has drained before the first upgrade",
			Description: `Checks (a) for each signing account, sampled at two heights H1 < H2, that the nonce at the ` +
				`height equals the "pending" nonce and did not move between the samples, and (b) that every --hash ` +
				`has a receipt. Check (c), that every successful hash's business effect is confirmed upstream, is ` +
				`manual. Exit code 0 when (a) and (b) hold, 2 when they do not, 1 on usage errors. See ` +
				`docs/signer-evm-upgrade-runbook.md.`,
			Flags:  []cli.Flag{signerEvmHashFlag, signerEvmAddressFlag, signerEvmBlockWaitFlag, signerEvmJSONFlag},
			Action: signerEvmPreflightAction,
		},
		{
			Name:  signer.EvmDisposeResolve,
			Usage: "Resolve a pending EVM transaction from on-chain evidence",
			Description: `Decides, at two heights H < H', what happened to the pending transaction --tx-hash of --scope, ` +
				`from its signed transaction evidence only: its receipt, else the sender's nonce N and the transaction ` +
				`that used N (--evidence-tx, --evidence-cosmos-tx, or --scan-blocks from the recorded block). A success ` +
				`is kept for the operation; a failure, or N used by a failed or provably unrelated transaction, lets ` +
				`the operation be signed again; the same call executed by another transaction is kept as its result. ` +
				`Anything unproven is refused, unless --accept-unknown (or --accept-gap for a nonce gap) turns the ` +
				`row into blocked_unknown, which keeps the operation blocked. A nonce gap is probed by re-sending the ` +
				`same bytes; --dry-run never sends. Exit codes: 0 done, 1 error, 2 not proven, 3 row gone or changed, ` +
				`4 invalid evidence. See docs/signer-evm-disposition.md.`,
			Flags: []cli.Flag{signerEvmScopeFlag, signerEvmTxHashFlag, signerEvmOperatorFlag, signerEvmReasonFlag,
				signerEvmEvidenceTxFlag, signerEvmEvidenceCosmosTxFlag, signerEvmScanBlocksFlag, signerEvmAcceptUnknownFlag,
				signerEvmAcceptGapFlag, signerEvmDryRunFlag, signerEvmBlockWaitFlag, signerEvmJSONFlag},
			Action: signerEvmDisposeAction(signer.EvmDisposeResolve),
		},
		{
			Name:  signer.EvmDisposeResolveOutcome,
			Usage: "Settle a blocked_unknown outcome once the chain proves what happened",
			Description: `Re-runs the resolve decision for blocked_unknown outcome --outcome-id from its evidence. ` +
				`A proven execution turns it into succeeded (or executed_elsewhere for a Cosmos transaction); a ` +
				`proven non-execution deletes it so the operation can be signed again. Anything else leaves it ` +
				`blocked (exit 2). No Board decision is needed: every exit rests on chain evidence.`,
			Flags: []cli.Flag{signerEvmOutcomeIDFlag, signerEvmOperatorFlag, signerEvmReasonFlag, signerEvmEvidenceTxFlag,
				signerEvmEvidenceCosmosTxFlag, signerEvmScanBlocksFlag, signerEvmDryRunFlag, signerEvmBlockWaitFlag, signerEvmJSONFlag},
			Action: signerEvmDisposeAction(signer.EvmDisposeResolveOutcome),
		},
		{
			Name:  signer.EvmDisposeRelease,
			Usage: "Release a blocked_unknown outcome without on-chain proof (needs a Board decision)",
			Description: `Deletes blocked_unknown outcome --outcome-id, so the operation can be signed again and may ` +
				`execute twice. Every release needs a Board decision on this one transaction (--decision-ref) and the ` +
				`business owner's verifiable evidence that the operation did not take effect (--business-evidence); ` +
				`there is no standing rule. The CLI records both but cannot check them. Exit code 3 when the outcome ` +
				`is gone or no longer blocked_unknown, 4 when its evidence is invalid.`,
			Flags: []cli.Flag{signerEvmOutcomeIDFlag, signerEvmOperatorFlag, signerEvmReasonFlag, signerEvmBusinessEvidenceFlag,
				signerEvmDecisionRefFlag, signerEvmDryRunFlag, signerEvmJSONFlag},
			Action: signerEvmDisposeAction(signer.EvmDisposeRelease),
		},
		{
			Name:  signer.EvmDisposeDisposeOutcome,
			Usage: "Dispose a succeeded or executed_elsewhere outcome the business no longer needs",
			Description: `Deletes succeeded or executed_elsewhere outcome --outcome-id once the business owner confirms ` +
				`the operation will not be retried, or that an identical new request must be signed. Exit code 3 ` +
				`when the outcome is gone or in another state.`,
			Flags:  []cli.Flag{signerEvmOutcomeIDFlag, signerEvmOperatorFlag, signerEvmReasonFlag, signerEvmDryRunFlag, signerEvmJSONFlag},
			Action: signerEvmDisposeAction(signer.EvmDisposeDisposeOutcome),
		},
	},
}

var (
	signerEvmScopeFlag      = &cli.StringFlag{Name: "scope", Usage: "The signing scope: operator, seal or gc"}
	signerEvmTxHashFlag     = &cli.StringFlag{Name: "tx-hash", Usage: "The pending transaction's hash, as 'signer-evm list' shows it"}
	signerEvmOutcomeIDFlag  = &cli.Uint64Flag{Name: "outcome-id", Usage: "The outcome's id, as 'signer-evm list' shows it"}
	signerEvmOperatorFlag   = &cli.StringFlag{Name: "operator", Usage: "Who runs the command (recorded in the audit)"}
	signerEvmReasonFlag     = &cli.StringFlag{Name: "reason", Usage: "Why (recorded in the audit)"}
	signerEvmEvidenceTxFlag = &cli.StringFlag{
		Name:  "evidence-tx",
		Usage: "An EVM transaction hash that used the pending transaction's nonce",
	}
	signerEvmEvidenceCosmosTxFlag = &cli.StringFlag{
		Name:  "evidence-cosmos-tx",
		Usage: "A Cosmos transaction hash that used the pending transaction's nonce (needs the node's tx index)",
	}
	signerEvmScanBlocksFlag = &cli.Uint64Flag{
		Name:  "scan-blocks",
		Usage: fmt.Sprintf("Scan up to N blocks from the recorded block, and search tx.acc_seq, for the transaction that used the nonce (at most %d)", signer.EvmDisposeMaxScanBlocks),
	}
	signerEvmAcceptUnknownFlag = &cli.BoolFlag{
		Name:  "accept-unknown",
		Usage: "When the outcome cannot be proven, turn the pending row into blocked_unknown (the operation stays blocked)",
	}
	signerEvmAcceptGapFlag = &cli.BoolFlag{
		Name:  "accept-gap",
		Usage: "When a nonce gap is confirmed, turn the pending row into blocked_unknown(nonce_gap) (the operation stays blocked)",
	}
	signerEvmDryRunFlag = &cli.BoolFlag{
		Name:  "dry-run",
		Usage: "Decide and print only: no DB change, no audit, no broadcast",
	}
	signerEvmBusinessEvidenceFlag = &cli.StringFlag{
		Name:  "business-evidence",
		Usage: "The business owner's verifiable evidence that the operation did not take effect",
	}
	signerEvmDecisionRefFlag = &cli.StringFlag{
		Name:  "decision-ref",
		Usage: "Link to the Board decision on this transaction",
	}
)

func parseSignerEvmHash(flag, value string) (ethcmn.Hash, error) {
	if value == "" {
		return ethcmn.Hash{}, nil
	}
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return ethcmn.Hash{}, fmt.Errorf("invalid --%s %q", flag, value)
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return ethcmn.Hash{}, fmt.Errorf("invalid --%s %q", flag, value)
	}
	return ethcmn.HexToHash(value), nil
}

func signerEvmDisposeAction(command string) cli.ActionFunc {
	return func(ctx *cli.Context) error {
		cfg, err := utils.MakeConfig(ctx)
		if err != nil {
			return err
		}
		req := signer.EvmDisposeRequest{
			Command: command, ChainID: cfg.Chain.ChainID, Scope: ctx.String(signerEvmScopeFlag.Name),
			OutcomeID: ctx.Uint64(signerEvmOutcomeIDFlag.Name), Operator: ctx.String(signerEvmOperatorFlag.Name),
			Reason: ctx.String(signerEvmReasonFlag.Name), BusinessEvidence: ctx.String(signerEvmBusinessEvidenceFlag.Name),
			DecisionRef: ctx.String(signerEvmDecisionRefFlag.Name), EvidenceCosmosTx: ctx.String(signerEvmEvidenceCosmosTxFlag.Name),
			ScanBlocks: ctx.Uint64(signerEvmScanBlocksFlag.Name), AcceptUnknown: ctx.Bool(signerEvmAcceptUnknownFlag.Name),
			AcceptGap: ctx.Bool(signerEvmAcceptGapFlag.Name), DryRun: ctx.Bool(signerEvmDryRunFlag.Name),
			BlockWait: ctx.Duration(signerEvmBlockWaitFlag.Name),
		}
		if req.TxHash, err = parseSignerEvmHash(signerEvmTxHashFlag.Name, ctx.String(signerEvmTxHashFlag.Name)); err != nil {
			return cli.Exit(err.Error(), signer.EvmDisposeExitError)
		}
		if req.EvidenceTx, err = parseSignerEvmHash(signerEvmEvidenceTxFlag.Name, ctx.String(signerEvmEvidenceTxFlag.Name)); err != nil {
			return cli.Exit(err.Error(), signer.EvmDisposeExitError)
		}
		store, err := openSignerEvmStore(cfg)
		if err != nil {
			return cli.Exit(err.Error(), signer.EvmDisposeExitError)
		}
		defer store.Close()
		if err = store.CheckSchema(ctx.Context); err != nil {
			return cli.Exit(err.Error(), signer.EvmDisposeExitError)
		}
		var evmChain signer.EvmDisposeChain
		var cosmosChain signer.CosmosDisposeChain
		if command == signer.EvmDisposeResolve || command == signer.EvmDisposeResolveOutcome {
			if len(cfg.Chain.RpcAddress) == 0 {
				return cli.Exit("no EVM RPC address configured ([Chain] RpcAddress)", signer.EvmDisposeExitError)
			}
			evmClient, err := ethclient.DialContext(ctx.Context, cfg.Chain.RpcAddress[0])
			if err != nil {
				return cli.Exit(err.Error(), signer.EvmDisposeExitError)
			}
			defer evmClient.Close()
			evmChain = signer.NewEvmDisposeChain(evmClient)
			if len(cfg.Chain.ChainAddress) != 0 {
				if cosmosChain, err = signer.NewCosmosDisposeChain(cfg.Chain.ChainAddress[0]); err != nil {
					return cli.Exit(err.Error(), signer.EvmDisposeExitError)
				}
			}
		}
		report := signer.DisposeEvmState(ctx.Context, store.DB(), req, evmChain, cosmosChain)
		return finishSignerEvmDispose(ctx, report)
	}
}

func finishSignerEvmDispose(ctx *cli.Context, report *signer.EvmDisposeReport) error {
	if err := printSignerEvmDispose(ctx.App.Writer, report, ctx.Bool(signerEvmJSONFlag.Name)); err != nil {
		return err
	}
	if err := logSignerEvmDispose(ctx.App.ErrWriter, report); err != nil {
		return err
	}
	if report.ExitCode != signer.EvmDisposeExitOK {
		return cli.Exit("", report.ExitCode)
	}
	return nil
}

// signerEvmDisposeLog is the one-line structured record of a disposition
// command on stderr, next to the audit row it wrote (if any). It carries no
// credentials and no transaction bytes.
type signerEvmDisposeLog struct {
	Msg        string `json:"msg"`
	Command    string `json:"command"`
	DryRun     bool   `json:"dry_run"`
	Scope      string `json:"scope,omitempty"`
	TxHash     string `json:"tx_hash,omitempty"`
	OutcomeID  uint64 `json:"outcome_id,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Action     string `json:"action,omitempty"`
	Applied    bool   `json:"applied"`
	AuditEvent string `json:"audit_event,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Actor      string `json:"actor"`
	// OperatorReason is --reason, as in the report; the audit row stores it as reason.
	OperatorReason string `json:"operator_reason,omitempty"`
	DecisionRef    string `json:"decision_ref,omitempty"`
	Error          string `json:"error,omitempty"`
}

func logSignerEvmDispose(w io.Writer, report *signer.EvmDisposeReport) error {
	return json.NewEncoder(w).Encode(signerEvmDisposeLog{
		Msg: "signer-evm disposition", Command: report.Command, DryRun: report.DryRun, Scope: report.Scope,
		TxHash: report.TxHash, OutcomeID: report.OutcomeID, Branch: report.Branch, Action: report.Action,
		Applied: report.Applied, AuditEvent: report.Event, ExitCode: report.ExitCode, Actor: report.Actor,
		OperatorReason: report.OperatorReason, DecisionRef: report.DecisionRef, Error: report.Error,
	})
}

func printSignerEvmDispose(w io.Writer, report *signer.EvmDisposeReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(w, "command: %s dry_run=%t\n", report.Command, report.DryRun)
	switch {
	case report.From != "":
		fmt.Fprintf(w, "transaction: scope=%s from=%s nonce=%d tx_hash=%s operation=%s\n", report.Scope, report.From,
			report.Nonce, report.TxHash, report.Operation)
	case report.TxHash != "":
		// the command stopped before the evidence was read
		fmt.Fprintf(w, "transaction: scope=%s tx_hash=%s\n", report.Scope, report.TxHash)
	}
	if report.OutcomeID != 0 {
		fmt.Fprintf(w, "outcome: id=%d state=%s reason=%s\n", report.OutcomeID, report.State, report.Reason)
	}
	for _, sample := range report.Samples {
		fmt.Fprintf(w, "height %d: chain_nonce=%d branch=%s", sample.Height, sample.ChainNonce, sample.Branch)
		if sample.PendingNonce != nil {
			fmt.Fprintf(w, " pending_nonce=%d", *sample.PendingNonce)
		}
		if sample.Occupier != nil {
			fmt.Fprintf(w, " occupier=%s:%s@%d succeeded=%t (%s)", sample.Occupier.Kind, sample.Occupier.Hash,
				sample.Occupier.Block, sample.Occupier.Succeeded, sample.Occupier.Source)
			if sample.Occupier.Operation != "" {
				fmt.Fprintf(w, " operation=%s", sample.Occupier.Operation)
			}
		}
		fmt.Fprintln(w)
		for _, note := range sample.Notes {
			fmt.Fprintf(w, "  note: %s\n", note)
		}
	}
	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
	if report.Action != "" {
		verb := "would"
		if report.Applied {
			verb = "done"
		}
		fmt.Fprintf(w, "action (%s): %s\n", verb, report.Action)
	}
	if report.Event != "" {
		fmt.Fprintf(w, "audit: %s\n", report.Event)
	}
	if report.Error != "" {
		fmt.Fprintf(w, "result: %s\n", report.Error)
	}
	fmt.Fprintf(w, "exit code: %d\n", report.ExitCode)
	return nil
}

func openSignerEvmStore(cfg *gfspconfig.GfSpConfig) (*sqldb.SignerEvmStore, error) {
	dbCfg := cfg.SpDB
	return sqldb.NewSignerEvmStore(&dbCfg)
}

func signerEvmMigrateAction(ctx *cli.Context) error {
	cfg, err := utils.MakeConfig(ctx)
	if err != nil {
		return err
	}
	store, err := openSignerEvmStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.Migrate(ctx.Context); err != nil {
		return err
	}
	if err = store.CheckSchema(ctx.Context); err != nil {
		return err
	}
	fmt.Fprintf(ctx.App.Writer, "signer EVM tables are ready: %s\n", strings.Join(sqldb.SignerEvmTableNames(), ", "))
	return nil
}

// signerEvmChainView is what list reads from the chain for one pending row.
type signerEvmChainView struct {
	Receipt     string `json:"receipt"`
	AccountNext string `json:"account_nonce"`
}

func signerEvmListAction(ctx *cli.Context) error {
	cfg, err := utils.MakeConfig(ctx)
	if err != nil {
		return err
	}
	store, err := openSignerEvmStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.CheckSchema(ctx.Context); err != nil {
		return err
	}
	report, err := signer.InspectEvmState(ctx.Context, store.DB(), cfg.Chain.ChainID)
	if err != nil {
		return err
	}
	chain := map[uint64]signerEvmChainView{}
	if len(report.Pending) != 0 && len(cfg.Chain.RpcAddress) != 0 {
		evmClient, dialErr := ethclient.DialContext(ctx.Context, cfg.Chain.RpcAddress[0])
		if dialErr == nil {
			defer evmClient.Close()
			for _, row := range report.Pending {
				chain[row.ID] = readSignerEvmChainView(ctx.Context, evmClient, row)
			}
		} else {
			fmt.Fprintf(ctx.App.ErrWriter, "chain state unavailable: %v\n", dialErr)
		}
	}
	if err = printSignerEvmReport(ctx.App.Writer, report, chain, ctx.Bool(signerEvmJSONFlag.Name)); err != nil {
		return err
	}
	for _, rows := range [][]signer.EvmStateRow{report.Pending, report.Outcomes} {
		for _, row := range rows {
			if !row.EvidenceValid {
				return cli.Exit("some rows have invalid evidence; they stay blocked until handled manually", signerEvmExitNotSatisfied)
			}
		}
	}
	return nil
}

func readSignerEvmChainView(ctx context.Context, evmClient *ethclient.Client, row signer.EvmStateRow) signerEvmChainView {
	view := signerEvmChainView{}
	receipt, err := evmClient.TransactionReceipt(ctx, ethcmn.HexToHash(row.TxHash))
	switch {
	case stderrors.Is(err, ethereum.NotFound):
		view.Receipt = "none"
	case err != nil:
		view.Receipt = "error: " + err.Error()
	case receipt.Status == types.ReceiptStatusSuccessful:
		view.Receipt = fmt.Sprintf("success@%d", receipt.BlockNumber.Uint64())
	default:
		view.Receipt = fmt.Sprintf("failed@%d", receipt.BlockNumber.Uint64())
	}
	nonce, err := evmClient.NonceAt(ctx, ethcmn.HexToAddress(row.From), nil)
	if err != nil {
		view.AccountNext = "error: " + err.Error()
	} else {
		view.AccountNext = fmt.Sprint(nonce)
	}
	return view
}

func printSignerEvmReport(w io.Writer, report *signer.EvmStateReport, chain map[uint64]signerEvmChainView, asJSON bool) error {
	if asJSON {
		out := struct {
			*signer.EvmStateReport
			Chain map[uint64]signerEvmChainView `json:"chain_state,omitempty"`
		}{report, chain}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Fprintf(w, "chain_id=%s evm_chain_id=%d\n", report.ChainID, report.EvmChainID)
	if len(report.ForeignChainIDs) != 0 {
		fmt.Fprintf(w, "WARNING: rows of other chain ids: %s\n", strings.Join(report.ForeignChainIDs, ", "))
	}
	if report.Lease == nil {
		fmt.Fprintln(w, "lease: none")
	} else {
		fmt.Fprintf(w, "lease: owner=%s epoch=%d expires_at=%s live=%t\n", report.Lease.Owner, report.Lease.Epoch,
			report.Lease.ExpiresAt.UTC().Format(time.RFC3339Nano), report.Lease.Live)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nPENDING\tID\tSCOPE\tNONCE\tTX_HASH\tAMBIGUOUS\tFROM\tOPERATION\tRECORDED_BLOCK\tEVIDENCE\tRECEIPT\tACCOUNT_NONCE")
	for _, row := range report.Pending {
		view := chain[row.ID]
		fmt.Fprintf(tw, "\t%d\t%s\t%d\t%s\t%t\t%s\t%s\t%d\t%s\t%s\t%s\n", row.ID, row.Scope, row.Nonce, row.TxHash, row.Ambiguous,
			row.From, row.Operation, row.RecordedBlock, evidenceLabel(row), view.Receipt, view.AccountNext)
	}
	fmt.Fprintln(tw, "\nOUTCOME\tOUTCOME_ID\tSCOPE\tSTATE\tREASON\tNONCE\tTX_HASH\tRESULT_TX_HASH\tEVIDENCE_TX\tOPERATION\tEVIDENCE")
	for _, row := range report.Outcomes {
		fmt.Fprintf(tw, "\t%d\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", row.ID, row.Scope, row.State, row.Reason, row.Nonce,
			row.TxHash, row.ResultTxHash, row.EvidenceTxHash, row.Operation, evidenceLabel(row))
	}
	return tw.Flush()
}

func evidenceLabel(row signer.EvmStateRow) string {
	if row.EvidenceValid {
		return "ok"
	}
	return "INVALID: " + row.EvidenceError
}

// preflightChain is the part of the EVM RPC preflight reads.
type preflightChain interface {
	BlockNumber(ctx context.Context) (uint64, error)
	NonceAt(ctx context.Context, account ethcmn.Address, blockNumber *big.Int) (uint64, error)
	PendingNonceAt(ctx context.Context, account ethcmn.Address) (uint64, error)
	TransactionReceipt(ctx context.Context, txHash ethcmn.Hash) (*types.Receipt, error)
}

type preflightAccount struct {
	Scope   string         `json:"scope"`
	Address ethcmn.Address `json:"address"`
}

type preflightSample struct {
	Height  uint64 `json:"height"`
	Nonce   uint64 `json:"nonce"`
	Pending uint64 `json:"pending"`
	Error   string `json:"error,omitempty"`
}

type preflightAccountResult struct {
	preflightAccount
	First     preflightSample `json:"first"`
	Second    preflightSample `json:"second"`
	Satisfied bool            `json:"satisfied"`
}

type preflightHashResult struct {
	Hash      string `json:"hash"`
	Receipt   string `json:"receipt"`
	Satisfied bool   `json:"satisfied"`
}

type preflightReport struct {
	Accounts []preflightAccountResult `json:"accounts"`
	Hashes   []preflightHashResult    `json:"hashes"`
	// CheckA and CheckB are §2.2 step 3 (a) and (b); (c) is manual.
	CheckA bool   `json:"check_a_nonce_stable"`
	CheckB bool   `json:"check_b_receipts"`
	Error  string `json:"error,omitempty"`
}

func (r *preflightReport) drained() bool { return r.CheckA && r.CheckB && r.Error == "" }

// runPreflight samples every account at H1 and at H2 >= H1+1: at each height
// it reads the block number first, then the nonce at that height and the
// "pending" nonce. Any read error counts as not satisfied.
func runPreflight(ctx context.Context, chain preflightChain, accounts []preflightAccount, hashes []ethcmn.Hash,
	blockWait, poll time.Duration,
) *preflightReport {
	report := &preflightReport{CheckA: true, CheckB: true}
	if err := checkPreflightScopes(accounts); err != nil {
		// A scope not sampled may still have pending work.
		report.Error, report.CheckA = err.Error(), false
		return report
	}
	sample := func(height uint64, account ethcmn.Address) preflightSample {
		s := preflightSample{Height: height}
		var err error
		if s.Nonce, err = chain.NonceAt(ctx, account, new(big.Int).SetUint64(height)); err != nil {
			s.Error = err.Error()
			return s
		}
		if s.Pending, err = chain.PendingNonceAt(ctx, account); err != nil {
			s.Error = err.Error()
		}
		return s
	}

	h1, err := chain.BlockNumber(ctx)
	if err != nil {
		report.Error = "eth_blockNumber: " + err.Error()
		report.CheckA = false
	}
	results := make([]preflightAccountResult, len(accounts))
	if report.Error == "" {
		for i, account := range accounts {
			results[i] = preflightAccountResult{preflightAccount: account, First: sample(h1, account.Address)}
		}
		h2 := h1
		deadline := time.Now().Add(blockWait)
		for h2 < h1+1 {
			if time.Now().After(deadline) {
				report.Error = fmt.Sprintf("no block after %d within %s", h1, blockWait)
				report.CheckA = false
				break
			}
			select {
			case <-ctx.Done():
				report.Error = ctx.Err().Error()
				report.CheckA = false
			case <-time.After(poll):
			}
			if report.Error != "" {
				break
			}
			if h2, err = chain.BlockNumber(ctx); err != nil {
				report.Error = "eth_blockNumber: " + err.Error()
				report.CheckA = false
				break
			}
		}
		for i, account := range accounts {
			if report.Error == "" {
				results[i].Second = sample(h2, account.Address)
			}
			first, second := results[i].First, results[i].Second
			results[i].Satisfied = report.Error == "" && first.Error == "" && second.Error == "" &&
				first.Pending == first.Nonce && second.Pending == second.Nonce && first.Nonce == second.Nonce
			report.CheckA = report.CheckA && results[i].Satisfied
		}
	}
	report.Accounts = results

	for _, hash := range hashes {
		result := preflightHashResult{Hash: hash.Hex()}
		receipt, err := chain.TransactionReceipt(ctx, hash)
		switch {
		case stderrors.Is(err, ethereum.NotFound):
			result.Receipt = "none"
		case err != nil:
			result.Receipt = "error: " + err.Error()
		case receipt == nil:
			result.Receipt = "none"
		default:
			result.Satisfied = true
			if receipt.Status == types.ReceiptStatusSuccessful {
				result.Receipt = fmt.Sprintf("success@%d", receipt.BlockNumber.Uint64())
			} else {
				result.Receipt = fmt.Sprintf("failed@%d", receipt.BlockNumber.Uint64())
			}
		}
		report.CheckB = report.CheckB && result.Satisfied
		report.Hashes = append(report.Hashes, result)
	}
	return report
}

func signerEvmPreflightAction(ctx *cli.Context) error {
	cfg, err := utils.MakeConfig(ctx)
	if err != nil {
		return err
	}
	accounts, err := preflightAccounts(cfg, ctx.StringSlice(signerEvmAddressFlag.Name))
	if err != nil {
		return cli.Exit(err.Error(), signerEvmExitError)
	}
	var hashes []ethcmn.Hash
	for _, value := range ctx.StringSlice(signerEvmHashFlag.Name) {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if len(item) != 66 || !strings.HasPrefix(item, "0x") {
				return cli.Exit(fmt.Sprintf("invalid --hash %q", item), signerEvmExitError)
			}
			hashes = append(hashes, ethcmn.HexToHash(item))
		}
	}
	if len(cfg.Chain.RpcAddress) == 0 {
		return cli.Exit("no EVM RPC address configured ([Chain] RpcAddress)", signerEvmExitError)
	}
	evmClient, err := ethclient.DialContext(ctx.Context, cfg.Chain.RpcAddress[0])
	if err != nil {
		return cli.Exit(err.Error(), signerEvmExitError)
	}
	defer evmClient.Close()

	report := runPreflight(ctx.Context, evmClient, accounts, hashes, ctx.Duration(signerEvmBlockWaitFlag.Name), time.Second)
	if err = printPreflightReport(ctx.App.Writer, report, ctx.Bool(signerEvmJSONFlag.Name)); err != nil {
		return err
	}
	if !report.drained() {
		return cli.Exit("the old signer has not drained: do not upgrade", signerEvmExitNotSatisfied)
	}
	return nil
}

// preflightScopes are the signing accounts preflight must cover, in order.
var preflightScopes = []string{"operator", "seal", "gc"}

// checkPreflightScopes requires accounts to hold each of preflightScopes
// exactly once and nothing else.
func checkPreflightScopes(accounts []preflightAccount) error {
	seen := map[string]bool{}
	for _, account := range accounts {
		if !slices.Contains(preflightScopes, account.Scope) {
			return fmt.Errorf("unknown scope %q, want one of %v", account.Scope, preflightScopes)
		}
		if seen[account.Scope] {
			return fmt.Errorf("scope %s given more than once", account.Scope)
		}
		seen[account.Scope] = true
	}
	for _, scope := range preflightScopes {
		if !seen[scope] {
			return fmt.Errorf("scope %s is not checked", scope)
		}
	}
	return nil
}

// preflightAccounts returns the operator, seal and gc accounts. An
// --address scope=0x.. flag sets one scope; every other scope is derived
// from its configured key (the signer's SIGNER_*_PRIV_KEY env vars override
// the config, as for the signer). A scope with neither is an error.
func preflightAccounts(cfg *gfspconfig.GfSpConfig, flags []string) ([]preflightAccount, error) {
	explicit := map[string]ethcmn.Address{}
	for _, value := range flags {
		for _, item := range strings.Split(value, ",") {
			scope, address, ok := strings.Cut(strings.TrimSpace(item), "=")
			if !ok || !ethcmn.IsHexAddress(address) {
				return nil, fmt.Errorf("invalid --address %q, want scope=0xADDRESS", item)
			}
			if !slices.Contains(preflightScopes, scope) {
				return nil, fmt.Errorf("invalid --address %q: unknown scope, want one of %v", item, preflightScopes)
			}
			if _, dup := explicit[scope]; dup {
				return nil, fmt.Errorf("invalid --address %q: scope %s given more than once", item, scope)
			}
			explicit[scope] = ethcmn.HexToAddress(address)
		}
	}
	keys := map[string]struct{ env, configured string }{
		"operator": {signer.SpOperatorPrivKey, cfg.SpAccount.OperatorPrivateKey},
		"seal":     {signer.SpSealPrivKey, cfg.SpAccount.SealPrivateKey},
		"gc":       {signer.SpGcPrivKey, cfg.SpAccount.GcPrivateKey},
	}
	accounts := make([]preflightAccount, 0, len(preflightScopes))
	for _, scope := range preflightScopes {
		if address, ok := explicit[scope]; ok {
			accounts = append(accounts, preflightAccount{Scope: scope, Address: address})
			continue
		}
		key := keys[scope]
		hexKey, err := util.SecretFromEnv(key.env, key.configured)
		if err != nil {
			return nil, err
		}
		if hexKey == "" {
			return nil, fmt.Errorf("no %s key configured; pass --address %s=0x..", scope, scope)
		}
		privateKey, err := crypto.HexToECDSA(hexKey)
		if err != nil {
			return nil, fmt.Errorf("invalid %s key: %w", scope, err)
		}
		accounts = append(accounts, preflightAccount{Scope: scope, Address: crypto.PubkeyToAddress(privateKey.PublicKey)})
	}
	return accounts, nil
}

func printPreflightReport(w io.Writer, report *preflightReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "(a) SCOPE\tADDRESS\tH1\tNONCE@H1\tPENDING1\tH2\tNONCE@H2\tPENDING2\tOK\tERROR")
	for _, a := range report.Accounts {
		errText := strings.TrimSpace(a.First.Error + " " + a.Second.Error)
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%t\t%s\n", a.Scope, a.Address.Hex(), a.First.Height, a.First.Nonce,
			a.First.Pending, a.Second.Height, a.Second.Nonce, a.Second.Pending, a.Satisfied, errText)
	}
	fmt.Fprintln(tw, "\n(b) HASH\tRECEIPT\tOK")
	for _, h := range report.Hashes {
		fmt.Fprintf(tw, "%s\t%s\t%t\n", h.Hash, h.Receipt, h.Satisfied)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if report.Error != "" {
		fmt.Fprintf(w, "error: %s\n", report.Error)
	}
	fmt.Fprintf(w, "\n(a) nonce stable: %t\n(b) every hash has a receipt: %t\n", report.CheckA, report.CheckB)
	fmt.Fprintln(w, "(c) manual: the business effect of every successful hash must be confirmed upstream and signed off")
	if report.drained() {
		fmt.Fprintln(w, "result: (a) and (b) hold; continue with (c)")
	} else {
		fmt.Fprintln(w, "result: NOT drained; do not upgrade")
	}
	return nil
}
