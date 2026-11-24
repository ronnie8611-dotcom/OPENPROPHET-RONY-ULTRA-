"""
OpenProphet Backtest Lab - FASE 1
Prueba las 4 estrategias reales de agent_tools.go en todos los timeframes.
"""
import os, json, time, warnings
from dataclasses import dataclass
import numpy as np
import pandas as pd
import yfinance as yf
from scipy import stats

warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"
os.makedirs(CACHE_DIR, exist_ok=True)

YF_TF = {"1m":"1m","5m":"5m","15m":"15m","30m":"30m","1h":"60m","1d":"1d"}
YF_PERIOD = {"1m":"7d","5m":"60d","15m":"60d","30m":"60d","1h":"730d","1d":"max"}
BARS_PER_HOUR = {"1m":60,"5m":12,"15m":4,"30m":2,"1h":1}
TIME_STOP = {"1m":30,"5m":6,"15m":2,"30m":1,"1h":1,"1d":5}

def load_config():
    with open("config_backtest.json","r",encoding="utf-8") as f:
        return json.load(f)

def download(symbol, tf):
    cache = os.path.join(CACHE_DIR, f"{symbol.replace('=','_')}_{tf}.csv")
    if os.path.exists(cache) and time.time()-os.path.getmtime(cache) < 24*3600:
        df = pd.read_csv(cache, index_col=0, parse_dates=True)
        print(f"  [cache] {len(df)} velas"); return df
    print(f"  [download] {symbol} {tf} period={YF_PERIOD[tf]}...", end=" ")
    try:
        df = yf.download(symbol, period=YF_PERIOD[tf], interval=YF_TF[tf],
                         progress=False, auto_adjust=False, threads=False)
    except Exception as e:
        print(f"ERR {e}"); return pd.DataFrame()
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.to_csv(cache); print(f"{len(df)} velas"); return df

def atr_series(df, n=14):
    h,l,c = df["high"],df["low"],df["close"]
    tr = pd.concat([h-l,(h-c.shift()).abs(),(l-c.shift()).abs()],axis=1).max(axis=1)
    return tr.rolling(n).mean()

def compute_ib(df, tf):
    df = df.copy()
    if not isinstance(df.index, pd.DatetimeIndex):
        df.index = pd.to_datetime(df.index, errors="coerce", utc=True)
    if getattr(df.index, "tz", None) is not None:
        df.index = df.index.tz_convert(None)
    df = df[df.index.notna()]
    df["date"] = df.index.date
    if tf == "1d":
        df["ib_high"] = df["high"].shift(1)
        df["ib_low"]  = df["low"].shift(1)
    else:
        n = BARS_PER_HOUR[tf]
        highs, lows = [], []
        for date, grp in df.groupby("date", sort=False):
            first = grp.head(n)
            ih, il = first["high"].max(), first["low"].min()
            for _ in range(len(grp)): highs.append(ih); lows.append(il)
        df["ib_high"] = highs; df["ib_low"] = lows
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    return df

@dataclass
class Signal:
    idx: int; direction: str; entry: float; stop_loss: float; take_profit: float

def strat_fit_mom(df, tf):
    df = compute_ib(df, tf); out=[]
    for i in range(20, len(df)):
        r = df.iloc[i]; p,ih,il,im = r["close"],r["ib_high"],r["ib_low"],r["ib_mid"]
        if pd.isna(ih) or pd.isna(il): continue
        if im < p < ih: out.append(Signal(i,"long",p,il,ih))
    return out

def strat_raschke(df, tf):
    df = compute_ib(df, tf); out=[]
    for i in range(20, len(df)):
        r = df.iloc[i]; p,ih,il,im = r["close"],r["ib_high"],r["ib_low"],r["ib_mid"]
        if pd.isna(ih) or pd.isna(il): continue
        if p >= ih*0.999:
            out.append(Signal(i,"long",ih,im,ih+(ih-il)*0.5))
        elif p <= il*1.001:
            out.append(Signal(i,"short",il,im,il-(ih-il)*0.5))
    return out

def strat_turtle(df, tf):
    out=[]; h20 = df["high"].rolling(20).max(); at = atr_series(df,14)
    for i in range(30, len(df)):
        p = df["close"].iloc[i]; hh = h20.iloc[i-1]; a = at.iloc[i]
        if pd.isna(hh) or pd.isna(a): continue
        if p > hh:
            out.append(Signal(i,"long",p,p-2*a,p+2*a))
    return out

def strat_mean_reversion(df, tf):
    out=[]
    sma = df["close"].rolling(20).mean()
    sd = df["close"].rolling(20).std(ddof=0)
    delta = df["close"].diff()
    gain = delta.where(delta > 0, 0).rolling(14).mean()
    loss = (-delta.where(delta < 0, 0)).rolling(14).mean()
    rs = gain / loss.replace(0, np.nan)
    rsi = 100 - (100 / (1 + rs))
    for i in range(30, len(df)):
        p, s, d, r = df["close"].iloc[i], sma.iloc[i], sd.iloc[i], rsi.iloc[i]
        if pd.isna(s) or pd.isna(d) or pd.isna(r) or d <= 0: continue
        upper, lower = s + 2*d, s - 2*d
        if p <= lower and r < 30:
            out.append(Signal(i,"long",p,p-d,s))
        elif p >= upper and r > 70:
            out.append(Signal(i,"short",p,p+d,s))
    return out


def strat_combined(df, tf):
    return []

STRATS = {"fit_mom":strat_fit_mom,"raschke":strat_raschke,
          "mean_reversion":strat_mean_reversion,
          "combined":strat_combined,"turtle_breakout":strat_turtle}

@dataclass
class Trade:
    entry_time: object; exit_time: object; direction: str
    entry_price: float; exit_price: float
    pnl: float; pnl_pct: float; reason: str; qty: float; tradeable: bool

def is_nq(s): return "NQ" in s.upper()

def size_position(capital, risk_pct, entry, sl, symbol, cfg_riesgo):
    max_pos = capital * cfg_riesgo["max_posicion_pct"] / 100
    mult = 20.0 if is_nq(symbol) and "MNQ" not in symbol.upper() else (2.0 if "MNQ" in symbol.upper() else 1.0)
    risk_usd = capital * risk_pct / 100
    dist = abs(entry - sl)
    if dist <= 0: return 0, mult
    risk_qty = risk_usd / (dist * mult)
    alloc_qty = max_pos / (entry * mult)
    qty = int(min(risk_qty, alloc_qty))
    return qty, mult

def simulate(df, sig, symbol, cfg, tf):
    risk_pct = cfg["estrategias"][cfg["_strat"]]["max_riesgo_pct"]
    capital = cfg["cuenta"]["capital_inicial_usd"]
    qty, mult = size_position(capital, risk_pct, sig.entry, sig.stop_loss, symbol, cfg["riesgo"])
    tradeable = qty >= 1

    slip = 0.01 if not is_nq(symbol) else 0.25
    comm = 0.005 if not is_nq(symbol) else 2.50

    entry = sig.entry + (slip if sig.direction=="long" else -slip)
    sl, tp = sig.stop_loss, sig.take_profit
    max_bars = TIME_STOP.get(tf, 10)

    px = entry; reason = "TIME"; j = sig.idx
    for j in range(sig.idx+1, min(sig.idx+1+max_bars, len(df))):
        b = df.iloc[j]; hi, lo = b["high"], b["low"]
        hit = None
        if sig.direction == "long":
            if lo <= sl: hit = ("SL", sl)
            elif hi >= tp: hit = ("TP", tp)
        else:
            if hi >= sl: hit = ("SL", sl)
            elif lo <= tp: hit = ("TP", tp)
        if hit:
            reason, px = hit; break
    else:
        j = min(sig.idx+max_bars, len(df)-1); px = df["close"].iloc[j]; reason = "TIME"

    gross = (px-entry) if sig.direction=="long" else (entry-px)
    net = gross * qty * mult - 2*comm*qty
    return Trade(df.index[sig.idx], df.index[j], sig.direction, entry, px,
                 net if tradeable else 0.0, (gross/entry*100) if entry else 0,
                 reason, qty, tradeable)

def deflated_sharpe(r, n_trials=48):
    if len(r)<3 or r.std()==0: return 0.0
    sr = r.mean()/r.std(); n=len(r)
    sk = stats.skew(r); ku = stats.kurtosis(r)
    emc = 0.5772156649
    var_sr = (1 - sk*sr + (ku-1)/4*sr**2)/(n-1)
    if var_sr<=0: return 0.0
    sr0 = np.sqrt(var_sr)*((1-emc)*stats.norm.ppf(1-1/n_trials) + emc*stats.norm.ppf(1-1/(n_trials*np.e)))
    return float(stats.norm.cdf((sr-sr0)/np.sqrt(var_sr)))

def metrics(trades, capital):
    ex = [t for t in trades if t.tradeable]
    if not ex:
        return {"n":0,"wr":0,"sharpe":0,"sortino":0,"calmar":0,"omega":0,
                "pf":0,"ret":0,"dd":0,"dsr":0,"n_untradeable":len(trades)}
    pnls = np.array([t.pnl for t in ex]); r = pnls/capital
    wins = pnls[pnls>0]; losses = pnls[pnls<0]
    eq = capital+np.cumsum(pnls); peak = np.maximum.accumulate(eq); dd = (eq-peak)/peak
    sr = r.mean()/r.std()*np.sqrt(252) if r.std()>0 else 0
    dn = r[r<0]; sortino = r.mean()/dn.std()*np.sqrt(252) if len(dn)>0 and dn.std()>0 else 0
    mdd = dd.min() if len(dd)>0 else 0
    calmar = (pnls.sum()/capital)/abs(mdd) if mdd else 0
    omega = (wins.sum()/abs(losses.sum())) if len(losses)>0 and losses.sum()!=0 else 999
    return {"n":len(ex),"wr":(pnls>0).mean(),"sharpe":sr,"sortino":sortino,
            "calmar":calmar,"omega":omega,"pf":omega,"ret":pnls.sum()/capital*100,
            "dd":mdd*100,"dsr":deflated_sharpe(r),"n_untradeable":len(trades)-len(ex)}

def run_walk_forward(df, strat_fn, cfg, tf):
    train_days = cfg["backtest"]["walk_forward"]["train_meses"]*30
    test_days  = cfg["backtest"]["walk_forward"]["test_meses"]*30
    trades = []
    if len(df) < train_days + test_days:
        for s in strat_fn(df, tf): trades.append(simulate(df,s,cfg["_symbol"],cfg,tf))
        return trades, "full"
    start = train_days
    while start + test_days <= len(df):
        wdf = df.iloc[start:start+test_days].copy()
        for s in strat_fn(wdf, tf):
            trades.append(simulate(wdf,s,cfg["_symbol"],cfg,tf))
        start += test_days
    return trades, "wfo"

def main():
    print("="*78); print("  OPENPROPHET BACKTEST LAB - FASE 1"); print("="*78)
    cfg = load_config()
    cap = cfg["cuenta"]["capital_inicial_usd"]
    syms, tfs = cfg["backtest"]["simbolos"], cfg["backtest"]["timeframes"]
    strats = list(cfg["estrategias"].keys())
    print(f"\nCapital: ${cap:,.0f} | Simbolos: {syms} | TFs: {tfs} | Estrategias: {strats}")
    print(f"Total backtests: {len(syms)*len(tfs)*len(strats)}\n")

    results = []
    for sym in syms:
        for tf in tfs:
            print(f"[{sym} / {tf}]"); df = download(sym, tf)
            if df.empty: continue
            for st in strats:
                allowed_tfs = cfg["estrategias"].get(st, {}).get("timeframes")
                if allowed_tfs and tf not in allowed_tfs: continue
                cfg["_symbol"]=sym; cfg["_timeframe"]=tf; cfg["_strat"]=st
                try:
                    tr, mode = run_walk_forward(df, STRATS[st], cfg, tf)
                except Exception as e:
                    print(f"  {st:16s} ERR {e}"); continue
                m = metrics(tr, cap)
                results.append({"symbol":sym,"tf":tf,"strategy":st,**m})
                print(f"  {st:16s} n={m['n']:3d} untradable={m['n_untradeable']:3d} "
                      f"WR={m['wr']*100:5.1f}% SR={m['sharpe']:6.2f} "
                      f"DSR={m['dsr']:4.2f} ret={m['ret']:7.2f}%")

    print("\n"+"="*78); print("  RESULTADOS (ordenado por Sharpe)"); print("="*78)
    if not results: print("Sin resultados."); return
    results.sort(key=lambda r:-r["sharpe"])
    hdr = f"{'Sym':6s} {'TF':4s} {'Strategy':16s} {'N':>3s} {'WR%':>6s} {'Sharpe':>7s} {'Sort':>6s} {'Calmar':>7s} {'PF':>5s} {'Ret%':>8s} {'DD%':>6s} {'DSR':>5s}"
    print(hdr); print("-"*len(hdr))
    for r in results[:40]:
        print(f"{r['symbol']:6s} {r['tf']:4s} {r['strategy']:16s} {r['n']:3d} "
              f"{r['wr']*100:6.1f} {r['sharpe']:7.2f} {r['sortino']:6.2f} "
              f"{r['calmar']:7.2f} {r['pf']:5.2f} {r['ret']:8.2f} {r['dd']:6.2f} {r['dsr']:5.2f}")
    with open("results_backtest.json","w",encoding="utf-8") as f:
        json.dump(results,f,indent=2,default=str)
    print("\nResultados completos: results_backtest.json")

if __name__ == "__main__": main()
